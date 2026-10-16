# Gemma-4 Fine-Tuning: Data Format & Transformation Pipeline

## Overview
This document outlines the structure of the training data used for fine-tuning the Gemma-4 model within the `gloflow_phoenix` codebase. The pipeline relies on a specific transformation process to convert structured task examples into a format compatible with Gemma-4's instruction-tuning requirements.

## 1. Raw Dataset Structure
The dataset is generated as a JSONL (JSON Lines) file. Each line represents an independent training example defined as a dictionary with the following keys:

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
The `format_instruction` function in `ai_train_core/prompt_format.py` transforms the raw JSON structure into a chat-template-compliant string. This template is crucial for the model to understand the conversation structure between the user and the assistant.

### Template Logic
The formatter uses turn markers to delimit the user input and the model output:

- **Markers:**
    - `<start_of_turn>user`: Marks the beginning of the user prompt.
    - `<start_of_turn>model`: Marks the beginning of the assistant response.
    - `<end_of_turn>`: Terminates a turn.

- **Transformation:**
```python
if input:
    # Formats with both instruction and input context
    "<start_of_turn>user\n{instruction}\n\n{input}<end_of_turn>\n<start_of_turn>model\n{output}<end_of_turn>"
else:
    # Formats with only instruction
    "<start_of_turn>user\n{instruction}<end_of_turn>\n<start_of_turn>model\n{output}<end_of_turn>"
```

## 3. Training Integration
The fine-tuning process follows these steps:

1.  **Ingestion**: `load_training_dataset()` (`training_common.py`) reads the raw JSONL file.
2.  **Formatting**: Each entry is mapped through the formatter to create a `"text"` field containing the template string.
3.  **Encoding**: The resulting list is converted into a Hugging Face `Dataset` object.
4.  **Training**: The `SFTTrainer` (Supervised Fine-Tuning) is initialized with `dataset_text_field="text"`, which instructs the trainer to use the formatted template strings for training.

This ensures that during training, the model learns to associate the user's intent within the `user` turn to the specific `model` response containing the requested tool-calling workflows.
