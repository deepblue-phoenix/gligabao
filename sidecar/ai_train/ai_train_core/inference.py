"""Generation with a fine-tuned adapter. Runs inside the GPU container for the given model size.

Loads with transformers + peft (model_load_infer.py), so it runs in the light inference image (requirements_infer.txt),
not the Unsloth training one.
"""
import gc
import os
import threading
import time
from typing import Callable, Optional

from .prompt_format import extract_model_response, format_inference_prompt
from .trained_model import resolve_model_dir

# The model stays loaded in the container's memory between requests, so only the first request of a container pays the
# load (the container itself stays up for the function's idle timeout, see FunctionSpec.idle_timeout_s). One model at
# a time: a request for another model or size replaces it, so the GPU never holds two.
_cache_lock = threading.Lock()
_cache: dict = {}   # "key": (model_size, adapter_dir, adapter mtime), "model", "tokenizer", "config", "adapter_dir", "loaded_at"


def _load(model_size: str, max_seq_length: int, load_in_4bit: bool, adapter_dir: Optional[str] = None):
    """-> (model, tokenizer, config, adapter_dir). Imported lazily: only the GPU images have torch/transformers."""
    from .model_load_infer import load_finetuned_model_for_inference
    return load_finetuned_model_for_inference(
        model_size=model_size, max_seq_length=max_seq_length, load_in_4bit=load_in_4bit, adapter_dir=adapter_dir)


def _adapter_stamp(model_size: str, adapter_dir: Optional[str]):
    """What identifies the weights on disk: a retrained adapter in the same directory gets a new stamp."""
    from .paths import FINETUNED_ADAPTER_DIR_TEMPLATE
    d = adapter_dir or FINETUNED_ADAPTER_DIR_TEMPLATE.format(model_size=model_size)
    try:
        return (model_size, d, os.stat(os.path.join(d, "adapter_config.json")).st_mtime_ns)
    except OSError:
        return (model_size, d, None)


def _drop_cached() -> None:
    """Free the loaded model (and its GPU memory)."""
    _cache.clear()
    gc.collect()
    try:
        import torch
        torch.cuda.empty_cache()
    except Exception:
        pass


def _get_model(model_size: str, adapter_dir: Optional[str]):
    """-> (model, tokenizer_or_processor, config, adapter_dir, cold): the cached model if it is the one asked for, else
    load it (replacing the cached one)."""
    key = _adapter_stamp(model_size, adapter_dir)
    if _cache.get("key") == key and key[2] is not None:
        print(f"Model already loaded in this container ({key[1]}): skipping the load")
        return _cache["model"], _cache["tokenizer"], _cache["config"], _cache["adapter_dir"], False
    _drop_cached()
    model, tokenizer, config, loaded_dir = _load(model_size, 2048, True, adapter_dir)
    _cache.update(key=_adapter_stamp(model_size, loaded_dir if adapter_dir else None), model=model, tokenizer=tokenizer,
                  config=config, adapter_dir=loaded_dir, loaded_at=time.time())
    return model, tokenizer, config, loaded_dir, True


def infer_response(prompt: str, max_tokens: int, temperature: float, model_size: str,
                      reload_fn: Optional[Callable] = None, model_path: str = "",
                      on_ready: Optional[Callable[[dict], None]] = None) -> dict:
    """Load base + a trained adapter and generate. Returns {"response", ...} or {"error"}.

    `model_path` picks the trained model (`trained/<job id>`, relative to the models volume, see trained_model.py);
    `model_size` must be the size it was trained for. Without it the legacy adapter of `model_size` is used.

    `reload_fn()` refreshes volumes so this container sees adapters committed by a training
    container that finished after this container's volumes were last synced.

    The loaded model is kept for the next request of this container (see `_cache`). `on_ready(info)` is called after
    each request with what the container holds (`model_size`, `adapter`, `loaded_at`, `last_used`, `requests`), so that
    it can be recorded for the admin UI.
    """
    if not prompt:
        return {"error": "prompt is required"}
    model_size = model_size.lower()
    if reload_fn:
        try:
            reload_fn()
        except Exception as e:   # e.g. the volume is busy: use what this container sees
            print(f"Reloading the models volume failed: {e!r}")

    with _cache_lock:
        return _infer_locked(prompt, max_tokens, temperature, model_size, model_path, on_ready)


def _infer_locked(prompt: str, max_tokens: int, temperature: float, model_size: str, model_path: str,
                     on_ready: Optional[Callable[[dict], None]]) -> dict:
    t0 = time.time()
    try:
        adapter_dir = resolve_model_dir(model_path) if model_path else None
        model, tokenizer_or_processor, config, adapter_dir, cold = _get_model(model_size, adapter_dir)
    except (FileNotFoundError, ValueError) as e:
        return {"error": str(e)}
    load_s = time.time() - t0

    tokenizer = getattr(tokenizer_or_processor, "tokenizer", tokenizer_or_processor)   # in case a processor is returned

    print(f"Generating with fine-tuned {config['display_name']} (adapter: {adapter_dir}) for: {prompt[:50]}...")
    import torch
    inputs = tokenizer(format_inference_prompt(prompt), return_tensors="pt").to(model.device)
    input_tokens = int(inputs["input_ids"].shape[-1])   # the formatted prompt, special tokens included
    with torch.no_grad():
        outputs = model.generate(
            **inputs,
            max_new_tokens=max_tokens,
            temperature=temperature,
            do_sample=True,
            pad_token_id=tokenizer.pad_token_id if tokenizer.pad_token_id is not None else tokenizer.eos_token_id,
        )
    output_tokens = int(outputs.shape[-1]) - input_tokens   # the newly generated tokens (EOS included)
    response = extract_model_response(tokenizer.decode(outputs[0], skip_special_tokens=True))
    print(f"Generated {len(response)} characters ({input_tokens} input tokens, {output_tokens} output tokens)")
    _cache["requests"] = _cache.get("requests", 0) + 1
    if on_ready:
        try:
            on_ready({"model_size": model_size, "model_path": model_path, "adapter": adapter_dir,
                      "loaded_at": _cache["loaded_at"], "last_used": time.time(), "requests": _cache["requests"]})
        except Exception as e:   # the record is only informational
            print(f"on_ready failed: {e!r}")
    return {"response": response, "model_size": model_size, "model_path": model_path, "adapter": adapter_dir,
            "cold_start": cold, "load_seconds": round(load_s, 2),
            "input_tokens": input_tokens, "output_tokens": output_tokens}
