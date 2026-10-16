"""Container-side mount points and file locations. No imports: safe to load anywhere.

`config.py` keys its volumes by these paths, and the training/inference code reads/writes
under them, so this is the single place they are defined.
"""

DATA_DIR = "/data"                                  # datasets
MODELS_DIR = "/models"                              # fine-tuned LoRA adapters
PROGRESS_DIR = "/progress"                          # training progress (jsonl)
EVAL_DIR = "/eval"                                  # eval examples collected via the web API
HF_CACHE_DIR = "/root/.cache/huggingface"           # base-model cache (avoids re-downloading)

DATASET_FILENAME = "gemma4_tool_calling_news_finance_v1.jsonl"
REMOTE_DATASET_PATH = f"{DATA_DIR}/{DATASET_FILENAME}"
REMOTE_METADATA_PATH = f"{DATA_DIR}/.metadata.json"
EVAL_DATASET_PATH = f"{EVAL_DIR}/eval_dataset.jsonl"
PROGRESS_FILE_TEMPLATE = PROGRESS_DIR + "/training_progress_{timestamp}.jsonl"

# Where fine-tuned adapters are saved, per model size (so 4b/12b never overwrite each other).
FINETUNED_ADAPTER_DIR_TEMPLATE = MODELS_DIR + "/finetuned_model_{model_size}"
