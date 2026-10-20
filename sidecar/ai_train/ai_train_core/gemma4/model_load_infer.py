"""Inference-only model loading for Gemma-4 4B, 12B and 31B: plain transformers + bitsandbytes + peft, no Unsloth.

This is what keeps the inference image light (requirements_infer.txt): the base model is loaded 4-bit (the same
quantization the model was trained with) and the trained LoRA adapter is attached on top. Training loads through
Unsloth instead (model_load.py, requirements_train.txt). The adapter files are the same for both: peft's format.

Imports torch/transformers/peft inside the function: this module itself is safe to import anywhere.
"""
from pathlib import Path

from .model_config import get_model_config
from ..paths import FINETUNED_ADAPTER_DIR_TEMPLATE


def load_finetuned_model_for_inference(model_size: str = "4b", max_seq_length: int = 2048,
                                       load_in_4bit: bool = True, adapter_dir: str = None):
    """Load the base model of `model_size` + a trained LoRA adapter for generation: the one in `adapter_dir` (a trained
    model's directory, see trained_model.resolve_model_dir), or the legacy per-size one (finetuned_model_<size>) without it.

    Raises FileNotFoundError if no adapter exists (deliberately no silent fallback to the un-tuned base model).
    `max_seq_length` is unused (kept so the signature matches the Unsloth loader): generation length is set per request.

    Returns (model, tokenizer, config, adapter_dir), model in eval mode.
    """
    config = get_model_config(model_size)
    adapter_dir = adapter_dir or FINETUNED_ADAPTER_DIR_TEMPLATE.format(model_size=model_size)
    if not (Path(adapter_dir) / "adapter_config.json").exists():
        raise FileNotFoundError(
            f"No fine-tuned LoRA adapter found at '{adapter_dir}'. "
            f"Run fine-tuning for model_size='{model_size}' first.")

    import torch
    from peft import PeftModel
    from transformers import AutoModelForCausalLM, AutoTokenizer, BitsAndBytesConfig

    print(f"\nLoading fine-tuned {config['display_name']} from {adapter_dir} (transformers + peft)...")
    quantization = BitsAndBytesConfig(
        load_in_4bit=True, bnb_4bit_quant_type="nf4", bnb_4bit_compute_dtype=torch.bfloat16,
        bnb_4bit_use_double_quant=True) if load_in_4bit else None
    base = AutoModelForCausalLM.from_pretrained(
        config["model_name"], quantization_config=quantization, torch_dtype=torch.bfloat16, device_map={"": 0})
    model = PeftModel.from_pretrained(base, adapter_dir, is_trainable=False)
    model.eval()
    try:   # the training job saves the tokenizer next to the adapter
        tokenizer = AutoTokenizer.from_pretrained(adapter_dir)
    except Exception:
        tokenizer = AutoTokenizer.from_pretrained(config["model_name"])
    print("Fine-tuned model loaded")
    return model, tokenizer, config, adapter_dir
