"""Gemma-4 model sizes and their training settings. Pure data: no heavy imports.

Both sizes train and serve through Unsloth (see model_load.py / model_train.py) in one image
(requirements_train.txt). google/gemma-4-12B-it ships the newer `gemma4_unified` architecture,
which Unsloth supports from 2026.10.x.
"""

MODEL_CONFIGS = {
    "4b": {
        "model_name": "google/gemma-4-E4B-it",
        "display_name": "Gemma-4 4B",
        "total_params": "4B",
        "trainable_params_r16": "~60M",
        "trainable_percent_r16": "1.5%",
        "recommended_gpu": "A10G (24GB)",
        "default_gpu": "a10g",
        "min_gpu_memory": "24GB",
        "recommended_batch_size": 2,
        "recommended_grad_accumulation": 4,
    },
    "12b": {
        "model_name": "google/gemma-4-12B-it",
        "display_name": "Gemma-4 12B",
        "total_params": "12B",
        "trainable_params_r16": "~180M",
        "trainable_percent_r16": "1.5%",
        "recommended_gpu": "A100 (40GB/80GB)",
        "default_gpu": "a100",
        "min_gpu_memory": "40GB",
        "recommended_batch_size": 1,
        "recommended_grad_accumulation": 8,
    },
}

MODEL_SIZES = tuple(MODEL_CONFIGS)


def get_model_config(model_size: str) -> dict:
    """Config for a model size ("4b" or "12b"). Raises ValueError otherwise."""
    key = (model_size or "").lower()
    if key not in MODEL_CONFIGS:
        raise ValueError(f"Invalid model_size '{model_size}'. Must be one of {list(MODEL_SIZES)}.")
    return MODEL_CONFIGS[key]
