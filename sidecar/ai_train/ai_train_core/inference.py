"""Generation with a fine-tuned adapter. Runs inside the GPU container for the given model size.

Loads through Unsloth, so it needs the Unsloth GPU image.
"""
from typing import Callable, Optional

from .prompt_format import extract_model_response, format_inference_prompt


def _load(model_size: str, max_seq_length: int, load_in_4bit: bool):
    """-> (model, tokenizer_or_processor, config, adapter_dir). Imported lazily: only the GPU image has unsloth."""
    from .model_load import load_finetuned_model_for_inference
    return load_finetuned_model_for_inference(
        model_size=model_size, max_seq_length=max_seq_length, load_in_4bit=load_in_4bit)


def generate_response(prompt: str, max_tokens: int, temperature: float, model_size: str,
                      reload_fn: Optional[Callable] = None) -> dict:
    """Load base + trained adapter for `model_size` and generate. Returns {"response", ...} or {"error"}.

    `reload_fn()` refreshes volumes so this container sees adapters committed by a training
    container that finished after this container's volumes were last synced.
    """
    if not prompt:
        return {"error": "prompt is required"}
    model_size = model_size.lower()
    if reload_fn:
        reload_fn()

    try:
        model, tokenizer_or_processor, config, adapter_dir = _load(model_size, 2048, True)
    except FileNotFoundError as e:
        return {"error": str(e)}

    # Gemma-4 (Unsloth path) returns a multimodal processor; generation needs the tokenizer.
    tokenizer = getattr(tokenizer_or_processor, "tokenizer", tokenizer_or_processor)

    print(f"Generating with fine-tuned {config['display_name']} (adapter: {adapter_dir}) for: {prompt[:50]}...")
    import torch
    inputs = tokenizer(format_inference_prompt(prompt), return_tensors="pt").to(model.device)
    with torch.no_grad():
        outputs = model.generate(
            **inputs,
            max_new_tokens=max_tokens,
            temperature=temperature,
            do_sample=True,
            pad_token_id=tokenizer.pad_token_id if tokenizer.pad_token_id is not None else tokenizer.eos_token_id,
        )
    response = extract_model_response(tokenizer.decode(outputs[0], skip_special_tokens=True))
    print(f"Generated {len(response)} characters")
    return {"response": response, "model_size": model_size, "adapter": adapter_dir}
