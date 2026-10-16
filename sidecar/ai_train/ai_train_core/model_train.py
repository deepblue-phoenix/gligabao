"""Fine-tuning pipeline for Gemma-4 via Unsloth (4B and 12B). Unsloth image only.
"""
import unsloth  # noqa: F401  must be imported before torch/trl/transformers/peft
from typing import Callable, Optional

from trl import SFTTrainer

from .model_config import get_model_config
from .model_load import add_lora_adapters, load_gemma4_model
from .paths import FINETUNED_ADAPTER_DIR_TEMPLATE, REMOTE_DATASET_PATH
from .progress import ProgressCallback
from .prompt_format import dataset_formatting_func
from .training_common import (load_training_dataset, print_dependency_versions, save_adapters,
                              setup_training_arguments, training_result)


def run_finetuning(num_train_steps: int = 100, model_size: str = "4b",
                   commit_fn: Optional[Callable] = None, dataset_path: str = REMOTE_DATASET_PATH,
                   job_id: str = "") -> dict:
    """load dataset -> load model (4-bit) -> add LoRA -> train -> save adapters.

    `commit_fn(paths)` persists volumes (progress during training, models at the end).
    Training cost on Modal is roughly $0.15 per 100 steps for 4B on an A10G; 500+ steps
    recommended for the ~325-example dataset.
    """
    config = get_model_config(model_size)
    print("=" * 60 + f"\nGEMMA-4 FINE-TUNING (UNSLOTH) - {config['display_name']}\n" + "=" * 60)

    examples, train_dataset = load_training_dataset(dataset_path)

    model, tokenizer_or_processor, model_config = load_gemma4_model(
        model_size=model_size, max_seq_length=2048, load_in_4bit=True)

    # Gemma-4 returns a Gemma4Processor; SFTTrainer needs the plain tokenizer (the processor
    # lacks `eos_token`), so pass it explicitly via processing_class.
    tokenizer = getattr(tokenizer_or_processor, "tokenizer", tokenizer_or_processor)

    model = add_lora_adapters(model, model_config=model_config, lora_rank=16, lora_alpha=16,
                              lora_dropout=0, use_gradient_checkpointing=True)

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
        callbacks=[ProgressCallback(commit_fn, job_id)],
    )
    train_result = trainer.train()

    print("\n" + "=" * 60 + "\nTRAINING COMPLETE\n" + "=" * 60)
    print(f"Final loss: {train_result.training_loss:.4f}, steps: {num_train_steps}, examples: {len(examples)}")

    save_adapters(model, tokenizer, FINETUNED_ADAPTER_DIR_TEMPLATE.format(model_size=model_size), commit_fn)
    return training_result(model_config, model_size, train_result, len(examples), num_train_steps)
