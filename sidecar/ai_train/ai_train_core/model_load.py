"""Unsloth model loading for Gemma-4 4B and 12B (training and inference).

Imports `unsloth` at module scope: only import this in the Unsloth image
(requirements_train.txt).
"""
from pathlib import Path

from unsloth import FastLanguageModel

from .model_config import MODEL_CONFIGS, get_model_config  # noqa: F401 (re-exported)
from .paths import FINETUNED_ADAPTER_DIR_TEMPLATE


def load_gemma4_model(model_size: str = "4b", max_seq_length: int = 2048, load_in_4bit: bool = True):
    """Load the base model from Hugging Face (cached on the HF cache volume), 4-bit by default.

    Returns (model, tokenizer_or_processor, config). Gemma-4 returns a multimodal
    Gemma4Processor rather than a plain tokenizer; see docs/training/tokenizers_and_processors.md.
    Needs HF_TOKEN (a secret, see SIDECAR_HF_SECRET) for gated models.
    """
    config = get_model_config(model_size)

    print(f"\n[2/5] Loading {config['display_name']} ({config['model_name']}), recommended GPU {config['recommended_gpu']}")
    model, tokenizer = FastLanguageModel.from_pretrained(
        model_name=config["model_name"],
        max_seq_length=max_seq_length,
        dtype=None,  # auto: bf16 where supported
        load_in_4bit=load_in_4bit,
    )
    print(f"Model loaded ({'4-bit' if load_in_4bit else 'full precision'})")
    return model, tokenizer, config


def add_lora_adapters(model, model_config: dict = None, lora_rank: int = 16, lora_alpha: int = 16,
                      lora_dropout: float = 0, use_gradient_checkpointing: bool = True):
    """Attach fresh LoRA adapters to the attention projections (q/k/v/o) and enter training mode.

    The base weights stay frozen; only the small adapter matrices train (~1.5% of parameters
    at r=16), which is what makes 4-bit fine-tuning fit on a 24GB GPU.
    """
    print("\n[3/5] Adding LoRA adapters...")
    model = FastLanguageModel.get_peft_model(
        model,
        r=lora_rank,
        target_modules=["q_proj", "k_proj", "v_proj", "o_proj"],
        lora_alpha=lora_alpha,
        lora_dropout=lora_dropout,
        bias="none",
        use_gradient_checkpointing=use_gradient_checkpointing,  # ~30-40% memory saving
    )
    model = FastLanguageModel.for_training(model)
    print(f"LoRA adapters applied (rank {lora_rank}, alpha {lora_alpha})")
    if model_config:
        print(f"  - Trainable parameters: {model_config['trainable_params_r16']} "
              f"({model_config['trainable_percent_r16']} of {model_config['total_params']})")
    return model


def load_finetuned_model_for_inference(model_size: str = "4b", max_seq_length: int = 2048,
                                       load_in_4bit: bool = True, adapter_dir: str = None):
    """Load base model + a trained LoRA adapter for generation: the one in `adapter_dir` (a trained model's directory,
    see trained_model.resolve_model_dir), or the legacy per-size one (finetuned_model_<size>) without it.

    Unsloth reads `base_model_name_or_path` from the adapter's adapter_config.json, loads
    that base model and attaches the trained weights. Raises FileNotFoundError if no adapter
    exists (deliberately no silent fallback to the un-tuned base model).

    Returns (model, tokenizer_or_processor, config, adapter_dir), model in inference mode.
    """
    config = get_model_config(model_size)
    adapter_dir = adapter_dir or FINETUNED_ADAPTER_DIR_TEMPLATE.format(model_size=model_size)
    if not (Path(adapter_dir) / "adapter_config.json").exists():
        raise FileNotFoundError(
            f"No fine-tuned LoRA adapter found at '{adapter_dir}'. "
            f"Run fine-tuning for model_size='{model_size}' first.")

    print(f"\nLoading fine-tuned {config['display_name']} from {adapter_dir}...")
    model, tokenizer = FastLanguageModel.from_pretrained(
        model_name=adapter_dir, max_seq_length=max_seq_length, dtype=None, load_in_4bit=load_in_4bit)
    FastLanguageModel.for_inference(model)  # optimized generation, no dropout
    print("Fine-tuned model loaded")
    return model, tokenizer, config, adapter_dir
