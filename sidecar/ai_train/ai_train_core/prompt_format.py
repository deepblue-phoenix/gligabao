"""Gemma-4 turn-marker prompt format shared by training and inference. No heavy imports.

    <start_of_turn>user
    {instruction}

    {input}<end_of_turn>
    <start_of_turn>model
    {output}<end_of_turn>

Training and inference must use the same format so the model sees prompts in the
distribution it was fine-tuned on. See docs/training/TRAINING_DATA_FORMAT.md.
"""

MODEL_TURN_MARKER = "<start_of_turn>model"


def format_instruction(example: dict) -> dict:
    """Raw example {instruction, input?, output} -> {"text": full training string}."""
    instruction = example["instruction"]
    input_text = example.get("input", "")
    output = example["output"]
    user = f"{instruction}\n\n{input_text}" if input_text else instruction
    return {"text": f"<start_of_turn>user\n{user}<end_of_turn>\n{MODEL_TURN_MARKER}\n{output}<end_of_turn>"}


def dataset_formatting_func(example: dict) -> str:
    """For SFTTrainer(formatting_func=...): the dataset already carries a formatted 'text' field.

    Module-level on purpose (picklable for datasets multiprocessing).
    """
    return example["text"]


def format_inference_prompt(prompt: str) -> str:
    """Prompt for generation: the user turn plus the opening of the model turn."""
    return f"<start_of_turn>user\n{prompt}<end_of_turn>\n{MODEL_TURN_MARKER}\n"


def extract_model_response(decoded: str) -> str:
    """Strip the echoed prompt from decoded generation output."""
    if MODEL_TURN_MARKER in decoded:
        decoded = decoded.split(MODEL_TURN_MARKER)[-1]
    return decoded.strip()
