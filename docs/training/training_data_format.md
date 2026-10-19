# Gemma-4 Fine-Tuning: Data Format & Transformation Pipeline

## Overview
This document outlines the structure of the training data used for fine-tuning the Gemma-4 model within the `gloflow_phoenix` codebase. The pipeline relies on a specific transformation process to convert structured task examples into a format compatible with Gemma-4's instruction-tuning requirements.

## 1. Raw Dataset Structure
The dataset is a JSONL (JSON Lines) file, found through its `dataset.toml` manifest in the training datasets directory (see [datasets.md](datasets.md): the layout, the manifest, and where the generator scripts live). Each line represents an independent training example defined as a dictionary with the following keys:

- `instruction` (str): The primary task or user query (e.g., "Find news about NVIDIA and buy 10 shares").
- `input` (str): Supplemental context or secondary parameters. Often an empty string.
- `output` (str): The comprehensive, multi-step response expected from the assistant, including tool usage details.

**Example Raw Entry:**
```json
{
  "instruction": "Check recent news about NVIDIA and buy 10 shares if it's positive",
  "input": "",
  "output": "I'll research NVIDIA news... [Tool calls and reasoning]"
}
```

## 2. The Transformation Pipeline
The `format_instruction` function in `ai_train_core/gemma4/prompt_format.py` transforms the raw JSON structure into a chat-template-compliant string. This template is crucial for the model to understand the conversation structure between the user and the assistant.

### Template Logic
The formatter uses **Gemma 4's own** turn markers (the ones in the base model's `chat_template.jinja`) to delimit the user input and the model output:

- **Markers:**
    - `<|turn>user`: Marks the beginning of the user prompt (`<|turn>` is the single token id 105).
    - `<|turn>model`: Marks the beginning of the assistant response.
    - `<turn|>`: Terminates a turn (single token id 106). It is the **end-of-turn / stop token**: it is in the base model's `generation_config.eos_token_id` (`[1, 106, 50]`), so a model that learned to write it after its answer stops there.

- **Not Gemma 4's markers:** `<start_of_turn>` / `<end_of_turn>` are Gemma 2/3's. They are not in Gemma 4's vocabulary, so they are tokenized as ordinary text (`<`, `start`, `_of`, ...). A model trained on them learns to *spell* `<end_of_turn>` after its answer, which is not a stop token, so generation does not stop and runs to `max_tokens` (rambling, inventing further turns). gligabao used them up to 0.46.0 (fixed in 0.46.1); adapters trained before that must be retrained.

- **Transformation:**
```python
if input:
    # Formats with both instruction and input context
    "<|turn>user\n{instruction}\n\n{input}<turn|>\n<|turn>model\n{output}<turn|>"
else:
    # Formats with only instruction
    "<|turn>user\n{instruction}<turn|>\n<|turn>model\n{output}<turn|>"
```

- **EOS in the data:** the dataset file (JSONL) has no EOS and should not: the end-of-turn token is added by `format_instruction` to every example, and the test eval checks (against the real Gemma 4 tokenizer) that the encoded text ends with id 106. `SFTTrainer` also appends the tokenizer's `eos_token` when the text does not already end with it (`<eos>`, id 1, for `google/gemma-4-*`; nothing for a tokenizer whose `eos_token` is `<turn|>`, such as Unsloth's); both ids stop generation.

- **BOS:** Gemma 4's chat template starts with `<bos>`, but the tokenizer does not necessarily add it (the raw `tokenizer.json` has no BOS post-processor). `bos_prefix(tokenizer)` returns `""` if the tokenizer already adds it and `<bos>` otherwise; `run_finetuning` prepends it to every training text (`add_text_prefix`) and inference prepends it to the prompt, so both have exactly one `<bos>`.

## 3. Training Integration
The fine-tuning process follows these steps:

1.  **Ingestion**: `load_training_dataset()` (`training_common.py`) reads the raw JSONL file.
2.  **Formatting**: Each entry is mapped through the formatter to create a `"text"` field containing the template string.
3.  **Encoding**: The resulting list is converted into a Hugging Face `Dataset` object.
4.  **Training**: The `SFTTrainer` (Supervised Fine-Tuning) is initialized with `formatting_func=dataset_formatting_func`, which returns the `"text"` field: the formatted template strings (with the `<bos>` prefix, see above) are what the trainer learns on.

This ensures that during training, the model learns to associate the user's intent within the `user` turn to the specific `model` response containing the requested tool-calling workflows.
