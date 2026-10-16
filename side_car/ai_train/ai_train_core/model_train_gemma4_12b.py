"""Fine-tuning and inference for google/gemma-4-12B-it WITHOUT Unsloth.

WHY THIS MODULE EXISTS
=============================================================================
`model_train.py` / `model_load.py` use Unsloth's `FastLanguageModel`, which works for the
4B model (`google/gemma-4-E4B-it`, model_type `gemma4`). The 12B instruct checkpoint was
re-published (2026-06-03) on a new architecture, `gemma4_unified`
(`Gemma4UnifiedForConditionalGeneration`, an encoder-free any-to-any multimodal refactor),
with its commit history squashed, so there is no older revision to pin to.

Unsloth (2026.7.2) cannot load it:
  1. `gemma4_unified` is registered in transformers only from 5.10.1 (absent in 5.6-5.9),
     but unsloth pins `transformers<=5.5.0`, so AutoConfig fails with
     `KeyError: 'gemma4_unified'`.
  2. Unsloth's loader only dispatches on the string "gemma4" and patches
     `transformers.models.gemma4.*`, a different module than `gemma4_unified.*`.
  3. Upstream: https://github.com/unslothai/unsloth/issues/5985 (same error) and
     https://github.com/unslothai/unsloth/issues/6028 (open: broken outputs even when
     transformers is force-upgraded).

So 12B is trained with plain `transformers` + `peft` + `trl` (bitsandbytes NF4 4-bit, manual
LoRA). Slower and heavier than Unsloth, but it works with the checkpoint Google publishes.
It runs in its own image (requirements_gemma4_12b.txt) that does not install unsloth, so this
module must NOT import `model_train` / `model_load` (both import unsloth at module scope).
Shared logic lives in unsloth-free modules (training_common, progress, prompt_format).

gemma4_unified notes (checked against transformers 5.10.1+):
  - It is in MODEL_FOR_CAUSAL_LM_MAPPING_NAMES, so `AutoModelForCausalLM` loads it; no need for
    the multimodal processor pipeline for a text-only LoRA fine-tune. `AutoTokenizer` works.
  - Attention/MLP use the standard Gemma names (q_proj, k_proj, v_proj, o_proj, gate_proj,
    up_proj, down_proj), so `LoraConfig(target_modules=[q,k,v,o])` works unmodified.
"""
from pathlib import Path
from typing import Callable, Optional

import torch
from packaging.version import Version
from peft import LoraConfig, PeftModel, get_peft_model, prepare_model_for_kbit_training
from transformers import AutoModelForCausalLM, AutoTokenizer, BitsAndBytesConfig
from trl import SFTTrainer

from .model_config import get_model_config
from .paths import FINETUNED_ADAPTER_DIR_TEMPLATE, REMOTE_DATASET_PATH
from .progress import ProgressCallback
from .prompt_format import dataset_formatting_func
from .training_common import (load_training_dataset, print_dependency_versions, save_adapters,
                              setup_training_arguments, training_result)

GEMMA4_12B_CONFIG = get_model_config("12b")
GEMMA4_12B_MODEL_NAME = GEMMA4_12B_CONFIG["model_name"]

# First transformers release that registers `gemma4_unified`.
MIN_TRANSFORMERS_VERSION = "5.10.1"


def _check_transformers_version() -> None:
    """Fail fast with a clear message instead of the upstream `KeyError: 'gemma4_unified'`."""
    import transformers

    if Version(transformers.__version__) < Version(MIN_TRANSFORMERS_VERSION):
        raise RuntimeError(
            f"{GEMMA4_12B_MODEL_NAME} requires transformers>={MIN_TRANSFORMERS_VERSION} "
            f"(model_type 'gemma4_unified'), but {transformers.__version__} is installed. "
            f"The 12B functions must use the image built from requirements_gemma4_12b.txt.")


def _dtype():
    return torch.bfloat16 if torch.cuda.is_bf16_supported() else torch.float16


def _load_base_model(load_in_4bit: bool):
    quantization_config = None
    if load_in_4bit:
        # NF4 4-bit: plain-transformers equivalent of Unsloth's load_in_4bit (~24GB -> ~6GB).
        quantization_config = BitsAndBytesConfig(
            load_in_4bit=True,
            bnb_4bit_compute_dtype=_dtype(),
            bnb_4bit_quant_type="nf4",
            bnb_4bit_use_double_quant=True,
        )
    return AutoModelForCausalLM.from_pretrained(
        GEMMA4_12B_MODEL_NAME, quantization_config=quantization_config,
        device_map="auto", torch_dtype=_dtype())


def load_gemma4_12b_model(max_seq_length: int = 2048, load_in_4bit: bool = True):
    """Load base google/gemma-4-12B-it via plain transformers. Returns (model, tokenizer, config)."""
    _check_transformers_version()
    print(f"\n[2/5] Loading {GEMMA4_12B_CONFIG['display_name']} (no Unsloth), "
          f"recommended GPU {GEMMA4_12B_CONFIG['recommended_gpu']}")

    model = _load_base_model(load_in_4bit)
    tokenizer = AutoTokenizer.from_pretrained(GEMMA4_12B_MODEL_NAME)
    tokenizer.model_max_length = max_seq_length
    if tokenizer.pad_token is None:
        tokenizer.pad_token = tokenizer.eos_token
    print(f"Model loaded ({'4-bit' if load_in_4bit else 'full precision'})")
    return model, tokenizer, GEMMA4_12B_CONFIG


def add_lora_adapters_no_unsloth(model, lora_rank: int = 16, lora_alpha: int = 16, lora_dropout: float = 0,
                                 use_gradient_checkpointing: bool = True):
    """Attach LoRA (q/k/v/o) via plain peft. Counterpart of model_load.add_lora_adapters."""
    print("\n[3/5] Adding LoRA adapters (no Unsloth)...")
    # Unsloth does k-bit prep internally (fp32 norms, input grads...); with plain peft it is explicit.
    model = prepare_model_for_kbit_training(model, use_gradient_checkpointing=use_gradient_checkpointing)
    model = get_peft_model(model, LoraConfig(
        r=lora_rank, lora_alpha=lora_alpha,
        target_modules=["q_proj", "k_proj", "v_proj", "o_proj"],
        lora_dropout=lora_dropout, bias="none", task_type="CAUSAL_LM"))
    model.train()
    print(f"LoRA adapters applied (rank {lora_rank}, alpha {lora_alpha})")
    return model


def run_finetuning_gemma4_12b(num_train_steps: int = 100, commit_fn: Optional[Callable] = None) -> dict:
    """load dataset -> load model (4-bit) -> add LoRA -> train -> save adapters (no Unsloth)."""
    print("=" * 60 + f"\nGEMMA-4 FINE-TUNING (NO UNSLOTH) - {GEMMA4_12B_CONFIG['display_name']}\n" + "=" * 60)

    examples, train_dataset = load_training_dataset(REMOTE_DATASET_PATH)
    model, tokenizer, model_config = load_gemma4_12b_model(max_seq_length=2048, load_in_4bit=True)
    model = add_lora_adapters_no_unsloth(model, lora_rank=16, lora_alpha=16, lora_dropout=0,
                                         use_gradient_checkpointing=True)

    training_args = setup_training_arguments(
        num_train_steps=num_train_steps,
        per_device_batch_size=model_config["recommended_batch_size"],
        gradient_accumulation_steps=model_config["recommended_grad_accumulation"],
        learning_rate=2e-4,
        model_config=model_config,
    )

    print("\n[5/5] Starting fine-tuning...")
    print_dependency_versions()
    trainer = SFTTrainer(
        model=model,
        processing_class=tokenizer,
        train_dataset=train_dataset,
        formatting_func=dataset_formatting_func,
        args=training_args,
        callbacks=[ProgressCallback(commit_fn)],
    )
    train_result = trainer.train()

    print("\n" + "=" * 60 + "\nTRAINING COMPLETE (NO UNSLOTH)\n" + "=" * 60)
    print(f"Final loss: {train_result.training_loss:.4f}, steps: {num_train_steps}, examples: {len(examples)}")

    # Same save path convention as the Unsloth pipeline, so inference finds the adapters
    # regardless of which training path produced them.
    save_adapters(model, tokenizer, FINETUNED_ADAPTER_DIR_TEMPLATE.format(model_size="12b"), commit_fn)
    return training_result(model_config, "12b", train_result, len(examples), num_train_steps)


def load_finetuned_gemma4_12b_for_inference(load_in_4bit: bool = True, adapter_dir: str = None,
                                            max_seq_length: int = 2048):
    """Base 12B model + trained LoRA adapter via peft. Returns (model, tokenizer, config, adapter_dir).

    Raises FileNotFoundError if no adapter exists (no silent fallback to the base model).
    """
    _check_transformers_version()
    adapter_dir = adapter_dir or FINETUNED_ADAPTER_DIR_TEMPLATE.format(model_size="12b")
    if not (Path(adapter_dir) / "adapter_config.json").exists():
        raise FileNotFoundError(
            f"No fine-tuned LoRA adapter found at '{adapter_dir}'. Run fine-tuning for model_size='12b' first.")

    print(f"\nLoading fine-tuned {GEMMA4_12B_CONFIG['display_name']} from {adapter_dir}...")
    model = PeftModel.from_pretrained(_load_base_model(load_in_4bit), adapter_dir)
    model.eval()
    tokenizer = AutoTokenizer.from_pretrained(adapter_dir)
    tokenizer.model_max_length = max_seq_length
    if tokenizer.pad_token is None:
        tokenizer.pad_token = tokenizer.eos_token
    return model, tokenizer, GEMMA4_12B_CONFIG, adapter_dir
