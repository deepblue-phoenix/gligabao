"""Gemma-4 turn-marker prompt format shared by training and inference. No heavy imports.

    <|turn>user
    {instruction}

    {input}<turn|>
    <|turn>model
    {output}<turn|>

These are Gemma 4's own markers (`<|turn>` id 105, `<turn|>` id 106; see the model's chat_template.jinja). They are NOT
the Gemma 2/3 markers `<start_of_turn>` / `<end_of_turn>`, which do not exist in Gemma 4's vocabulary: tokenized there
they are plain text, the model would learn to write them as characters and never emit the real stop token (id 106, one of
`generation_config.eos_token_id` = [1, 106, 50]), so replies would run to max_tokens.

Training and inference must use the same format so the model sees prompts in the distribution it was fine-tuned on.
See docs/training/TRAINING_DATA_FORMAT.md.

BOS: Gemma 4's chat template starts with `<bos>`, but its tokenizer may not add it by itself. `bos_prefix(tokenizer)`
says whether the text needs it; train and infer both call it, so they agree.
"""

TURN_START = "<|turn>"
TURN_END = "<turn|>"          # ends a turn; the token generation stops on (id 106)
EOS_TOKEN = "<eos>"           # id 1
MODEL_TURN_MARKER = f"{TURN_START}model"
STOP_TOKENS = (TURN_END, EOS_TOKEN)


def format_instruction(example: dict) -> dict:
    """Raw example {instruction, input?, output} -> {"text": full training string}."""
    instruction = example["instruction"]
    input_text = example.get("input", "")
    output = example["output"]
    user = f"{instruction}\n\n{input_text}" if input_text else instruction
    return {"text": f"{TURN_START}user\n{user}{TURN_END}\n{MODEL_TURN_MARKER}\n{output}{TURN_END}"}


def dataset_formatting_func(example: dict) -> str:
    """For SFTTrainer(formatting_func=...): the dataset already carries a formatted 'text' field.

    Module-level on purpose (picklable for datasets multiprocessing).
    """
    return example["text"]


def format_inference_prompt(prompt: str) -> str:
    """Prompt for generation: the user turn plus the opening of the model turn."""
    return f"{TURN_START}user\n{prompt}{TURN_END}\n{MODEL_TURN_MARKER}\n"


def bos_prefix(tokenizer) -> str:
    """"" when `tokenizer(text)` already starts with BOS (or the tokenizer has none), else the BOS token to put in front.

    Used for the training text and the inference prompt alike, so exactly one BOS ends up in both.
    """
    bos_token, bos_id = getattr(tokenizer, "bos_token", None), getattr(tokenizer, "bos_token_id", None)
    if not bos_token or bos_id is None:
        return ""
    first = tokenizer("x")["input_ids"]
    return "" if first and first[0] == bos_id else bos_token


def stop_token_ids(tokenizer) -> list:
    """Ids generation must stop on: the end-of-turn token and EOS, those that exist in the vocabulary."""
    unk = getattr(tokenizer, "unk_token_id", None)
    ids = []
    for token in STOP_TOKENS:
        token_id = tokenizer.convert_tokens_to_ids(token)
        if token_id is not None and token_id != unk and token_id not in ids:
            ids.append(token_id)
    return ids


def extract_model_response(decoded: str) -> str:
    """Strip an echoed prompt (if any) and a trailing end-of-turn marker from decoded generation output."""
    if MODEL_TURN_MARKER in decoded:
        decoded = decoded.split(MODEL_TURN_MARKER)[-1]
    decoded = decoded.strip()
    stripped = True
    while stripped:
        stripped = False
        for token in STOP_TOKENS:
            if decoded.endswith(token):
                decoded, stripped = decoded[: -len(token)].rstrip(), True
    return decoded
