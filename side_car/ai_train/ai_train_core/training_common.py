"""Training helpers shared by the Unsloth (4B) and plain-transformers (12B) pipelines.

Heavy libraries are imported lazily inside functions, so this module is safe to import
before `unsloth` (which must be imported before torch/trl/transformers/peft).
"""
import json
from pathlib import Path
from typing import Callable, Optional

from .paths import MODELS_DIR, REMOTE_DATASET_PATH
from .prompt_format import format_instruction


def print_dependency_versions(libs=("torch", "transformers", "trl", "peft", "accelerate", "bitsandbytes", "unsloth")):
    print("\n" + "=" * 60 + "\nDEPENDENCY VERSIONS\n" + "=" * 60)
    for lib in libs:
        try:
            version = __import__(lib).__version__
        except ImportError:
            continue  # not installed in this image (e.g. unsloth in the 12B image)
        except Exception as e:
            version = f"Error: {e}"
        print(f"  {lib:<20} {version}")
    print("=" * 60 + "\n")


def load_training_dataset(dataset_path: str = REMOTE_DATASET_PATH):
    """Load the JSONL dataset and format it. Returns (raw_examples, datasets.Dataset with 'text')."""
    from datasets import Dataset

    print("[1/5] Loading dataset...")
    path = Path(dataset_path)
    if not path.exists():
        raise FileNotFoundError(f"Dataset not found at {path}. Run the 'sync' action first.")

    with open(path) as f:
        examples = [json.loads(line) for line in f if line.strip()]
    print(f"Loaded {len(examples)} training examples")

    dataset = Dataset.from_list([format_instruction(ex) for ex in examples])
    print(f"Formatted {len(dataset)} examples for training")
    return examples, dataset


def setup_training_arguments(num_train_steps: int, per_device_batch_size: int, gradient_accumulation_steps: int,
                             learning_rate: float = 2e-4, model_config: Optional[dict] = None):
    """SFTConfig for LoRA fine-tuning.

    - learning_rate 2e-4: LoRA tolerates a higher LR than full fine-tuning (1e-5..5e-5).
    - warmup_steps 10, linear schedule, weight_decay 0.01, adamw_8bit (memory-efficient optimizer).
    - bf16 when the GPU supports it, fp16 otherwise.
    - dataset_num_proc=1: with the current transformers 5.x / torch / Gemma-4 tokenizer stack,
      datasets' multiprocessing `.map()` fails pickling the tokenizer
      (`TypeError: cannot pickle 'ConfigModuleInstance' object`). Single process is safe and
      costs nothing for a few hundred examples; revisit for large datasets once fixed upstream.
    """
    import torch
    from trl import SFTConfig

    print("\n[4/5] Configuring training...")
    if model_config:
        print(f"  - Model: {model_config['display_name']}")
        print(f"  - Recommended batch/accum: {model_config['recommended_batch_size']}/"
              f"{model_config['recommended_grad_accumulation']}")
    print(f"  - Batch size: {per_device_batch_size}, grad accumulation: {gradient_accumulation_steps}, "
          f"effective: {per_device_batch_size * gradient_accumulation_steps}")

    bf16 = torch.cuda.is_bf16_supported()
    return SFTConfig(
        output_dir="/tmp/outputs",
        per_device_train_batch_size=per_device_batch_size,
        gradient_accumulation_steps=gradient_accumulation_steps,
        warmup_steps=10,
        max_steps=num_train_steps,
        learning_rate=learning_rate,
        fp16=not bf16,
        bf16=bf16,
        logging_steps=5,
        optim="adamw_8bit",
        weight_decay=0.01,
        lr_scheduler_type="linear",
        seed=42,
        report_to="none",
        dataset_num_proc=1,
    )


def save_adapters(model, tokenizer, save_path: str, commit_fn: Optional[Callable] = None) -> None:
    """Save LoRA adapters (small, ~64MB+) + tokenizer to the models volume and persist it."""
    model.save_pretrained(save_path)
    tokenizer.save_pretrained(save_path)
    if commit_fn:
        commit_fn([MODELS_DIR])
    print(f"Model adapters saved to {save_path} and committed to volume")


def training_result(model_config: dict, model_size: str, train_result, num_examples: int, num_steps: int) -> dict:
    return {
        "status": "success",
        "model": model_config["model_name"],
        "model_size": model_size,
        "final_loss": float(train_result.training_loss),
        "num_examples": num_examples,
        "num_steps": num_steps,
    }
