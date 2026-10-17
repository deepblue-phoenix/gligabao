"""Container-side mount points and file locations. No imports: safe to load anywhere.

`config.py` keys its volumes by these paths, and the training/inference code reads/writes
under them, so this is the single place they are defined.
"""

DATA_DIR = "/data"                                  # datasets
MODELS_DIR = "/models"                              # fine-tuned LoRA adapters
EVAL_DIR = "/eval"                                  # eval examples collected via the web API
HF_CACHE_DIR = "/root/.cache/huggingface"           # base-model cache (avoids re-downloading)

DATASET_FILENAME = "gemma4_tool_calling_news_finance_v1.jsonl"
REMOTE_DATASET_PATH = f"{DATA_DIR}/{DATASET_FILENAME}"
REMOTE_METADATA_PATH = f"{DATA_DIR}/.metadata.json"
EVAL_DATASET_PATH = f"{EVAL_DIR}/eval_dataset.jsonl"

# Every training job saves its weights (a LoRA adapter: a few files) in a directory of its own, named after the job, so
# training again never overwrites an earlier model. The path relative to MODELS_DIR is what the Go server records
# (gb_trained_models.path) and what inference is asked to load (`model_path`).
TRAINED_MODELS_SUBDIR = "trained"
TRAINED_MODEL_DIR_TEMPLATE = MODELS_DIR + "/" + TRAINED_MODELS_SUBDIR + "/{job_id}"

# Where the single adapter per model size was saved before jobs had directories of their own. Inference without a
# `model_path` still loads it (a model trained by an older version).
FINETUNED_ADAPTER_DIR_TEMPLATE = MODELS_DIR + "/finetuned_model_{model_size}"
