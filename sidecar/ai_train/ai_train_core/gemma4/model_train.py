"""Fine-tuning pipeline for Gemma-4 via Unsloth (4B and 12B). Unsloth image only.
"""
import unsloth  # noqa: F401  must be imported before torch/trl/transformers/peft
import time
from typing import Callable, Optional

from trl import SFTTrainer

from .model_config import get_model_config
from .model_load import add_lora_adapters, load_gemma4_model
from ..paths import REMOTE_DATASET_PATH
from ..progress import ProgressCallback
from ..event_log import EventLog
from ..progress_events import JobCancelled, stop_requested, write_log
from .prompt_format import bos_prefix, dataset_formatting_func, format_instruction
from ..trained_model import describe, model_dir
from ..training_common import (add_text_prefix, load_training_dataset, print_dependency_versions, save_adapters,
                              setup_training_arguments, training_result)


def run_finetuning(num_train_steps: int = 100, model_size: str = "4b",
                   commit_fn: Optional[Callable] = None, dataset_path: str = REMOTE_DATASET_PATH,
                   job_id: str = "", events: Optional[EventLog] = None) -> dict:
    """load dataset -> load model (4-bit) -> add LoRA -> train -> save adapters.

    The adapters are saved in a directory of this job (trained_model.model_dir), and the result has their description
    under "model" (trained_model.describe); bootstrap.run_train reports it with the "completed" event.

    `commit_fn(paths)` persists the models volume at the end.
    Training cost on Modal is roughly $0.15 per 100 steps for 4B on an A10G; 500+ steps
    recommended for the ~325-example dataset.

    The stages are reported as "log" events of the job; between them (and between the steps, see ProgressCallback) a
    stop request (progress_events.py) ends the job with JobCancelled. `events` is the job's EventLog (None: nothing is
    reported).
    """
    t0 = time.time()

    def stage(message: str) -> None:
        """Report a stage, and stop here if the job was asked to."""
        write_log(events, f"{message} ({time.time() - t0:.0f}s since the start)")
        if stop_requested(events):
            raise JobCancelled(f"stopped: {message}")

    config = get_model_config(model_size)
    print("=" * 60 + f"\nGEMMA-4 FINE-TUNING (UNSLOTH) - {config['display_name']}\n" + "=" * 60)

    examples, train_dataset = load_training_dataset(format_instruction, dataset_path)
    stage(f"dataset loaded: {len(examples)} examples; loading the {config['display_name']} model (4-bit)")

    max_seq_length, lora_rank, lora_alpha, learning_rate = 2048, 16, 16, 2e-4
    model, tokenizer_or_processor, model_config = load_gemma4_model(
        model_size=model_size, max_seq_length=max_seq_length, load_in_4bit=True)

    # Gemma-4 returns a Gemma4Processor; SFTTrainer needs the plain tokenizer (the processor
    # lacks `eos_token`), so pass it explicitly via processing_class.
    tokenizer = getattr(tokenizer_or_processor, "tokenizer", tokenizer_or_processor)

    # Exactly one <bos> at the start of every example, as inference does (see prompt_format.bos_prefix).
    prefix = bos_prefix(tokenizer)
    train_dataset = add_text_prefix(train_dataset, prefix)
    print(f"BOS prefix added to the training text: {prefix!r}")

    stage("model loaded; adding the LoRA adapters")
    model = add_lora_adapters(model, model_config=model_config, lora_rank=lora_rank, lora_alpha=lora_alpha,
                              lora_dropout=0, use_gradient_checkpointing=True)

    training_args = setup_training_arguments(
        num_train_steps=num_train_steps,
        per_device_batch_size=model_config["recommended_batch_size"],
        gradient_accumulation_steps=model_config["recommended_grad_accumulation"],
        learning_rate=learning_rate,
        model_config=model_config,
    )

    stage(f"setting up the trainer (batch {training_args.per_device_train_batch_size} x "
          f"accumulation {training_args.gradient_accumulation_steps})")
    print("\n[5/5] Starting fine-tuning...")
    print_dependency_versions()
    trainer = SFTTrainer(
        model=model,
        processing_class=tokenizer,
        train_dataset=train_dataset,
        formatting_func=dataset_formatting_func,
        args=training_args,
        callbacks=[ProgressCallback(events)],
    )
    stage("trainer ready; training starts (the first step is slow: kernels compile)")
    train_result = trainer.train()

    print("\n" + "=" * 60 + "\nTRAINING COMPLETE\n" + "=" * 60)
    print(f"Final loss: {train_result.training_loss:.4f}, steps: {num_train_steps}, examples: {len(examples)}")

    stage("training done; saving the adapters")
    save_dir = model_dir(job_id)
    save_adapters(model, tokenizer, save_dir, commit_fn)
    result = training_result(model_config, model_size, train_result, len(examples), num_train_steps)
    result["model"] = describe(
        save_dir, base_model=model_config["model_name"], model_size=model_size, num_examples=len(examples),
        dataset_path=dataset_path,
        params={"num_train_steps": num_train_steps, "lora_rank": lora_rank, "lora_alpha": lora_alpha, "lora_dropout": 0,
                "learning_rate": learning_rate, "max_seq_length": max_seq_length, "load_in_4bit": True,
                "batch_size": training_args.per_device_train_batch_size,
                "gradient_accumulation_steps": training_args.gradient_accumulation_steps})
    return result
