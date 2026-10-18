# How the Gemma-4 models work in gligabao

What happens to a prompt between the admin UI and the generated text, and why a request takes the time it takes. The page
describes **what gligabao's code does** (each fact points to the file that does it) and says plainly where something
about Gemma 4 itself is **not verified** (marked *Unverified*): those need a check against the model card or in a container
before they are relied on. For sizes, GPUs and costs see [MODEL_SIZES.md](MODEL_SIZES.md); for the data format see
[TRAINING_DATA_FORMAT.md](TRAINING_DATA_FORMAT.md); for the Modal side (containers, cold and warm starts) see
[Cold and warm starts](../modal.md#cold-and-warm-starts).

## 1. The pieces

| Piece | What it is | Where |
|---|---|---|
| Base model | `google/gemma-4-E4B-it` (4b) or `google/gemma-4-12B-it` (12b): an instruction-tuned decoder-only language model. Downloaded once into the HF cache volume. | `gemma4/model_config.py` |
| LoRA adapter | Small extra matrices (rank 16, alpha 16, dropout 0) on the attention projections `q_proj`, `k_proj`, `v_proj`, `o_proj`. The only thing a training job saves. The base weights stay frozen. | `gemma4/model_load.py`, `gemma4/model_train.py` |
| Tokenizer | Turns text into token ids and back. Saved next to the adapter; inference reads it from there (the base model's if absent). | `training_common.py`, `model_load_infer.py` |
| Quantization | The base model is held in 4 bits (nf4, double quantization, bfloat16 compute) both in training (Unsloth) and in inference (bitsandbytes), so the adapter is used with the same kind of weights it was trained on. | `model_load.py`, `model_load_infer.py` |

*Unverified:* what the **"E4B"** in the 4b model's name means for its size. The code and `MODEL_SIZES.md` treat it as a 4
billion parameter model (about 8GB in 16 bits); the model card says how many parameters the file really has and how many
are "effective". It matters for download and load time, not for the reasoning below.

## 2. The prompt

gligabao does **not** use the tokenizer's chat template. It builds the text itself (`gemma4/prompt_format.py`) with Gemma 4's turn markers, the same ones the template uses (checked against `google/gemma-4-E4B-it`'s `chat_template.jinja`, `tokenizer.json` and `generation_config.json`):

```
training text   <bos><|turn>user\n{instruction}\n\n{input}<turn|>\n<|turn>model\n{output}<turn|>
inference text  <bos><|turn>user\n{prompt}<turn|>\n<|turn>model\n
```

`<|turn>` (id 105) and `<turn|>` (id 106) are single special tokens. `<bos>` is put in front only if the tokenizer does not add it itself (`bos_prefix`; the raw Gemma 4 `tokenizer.json` does not), identically in training and inference.

The inference text is the training text cut right after `<|turn>model\n`: the model is asked to continue as the model turn, and was trained to end it with `<turn|>`. Training and inference use the same functions, so they agree with each other, and with the base model's own template.

**History (fixed in 0.46.1).** Before, the text used Gemma 2/3's `<start_of_turn>` / `<end_of_turn>`. They do not exist in Gemma 4's vocabulary, so they were tokenized as plain text: the model learned to write the characters `<end_of_turn>` after an answer instead of the stop token, and replies ran to `max_tokens` with invented continuations. Adapters trained before 0.46.1 have this problem and must be retrained. A check that needs only the `tokenizers` library (the tests do it, `Gemma4TokenizerTest`):

```python
from tokenizers import Tokenizer
tok = Tokenizer.from_pretrained("google/gemma-4-E4B-it")
print(tok.encode("<turn|>", add_special_tokens=False).ids)          # [106]: one special token
print(tok.encode("<end_of_turn>", add_special_tokens=False).ids)    # several ids: plain text, not a marker
```

The training text is a single string per example, and the trainer (`SFTTrainer` with `formatting_func`) learns on **all**
of it, the user turn too: the code does not mask the prompt (no "train on responses only").

## 3. When generation stops

The model produces one token at a time. `model.generate(...)` (`inference.py`) stops when **either**

1. the model emits a stop token. gligabao passes `eos_token_id` itself (`stop_token_ids`: `<turn|>` 106 and `<eos>` 1, those that exist in the tokenizer's vocabulary); the base model's `generation_config.eos_token_id` is `[1, 106, 50]`, **or**
2. `max_new_tokens` tokens were produced (`max_tokens` of the request; admin UI: *max tokens*).

There is no other stop. So the length of a reply is whatever the model decides, bounded by `max_tokens`. If the model does
not emit a token of case 1 after its answer, it goes on (often rambling or repeating) until the limit, which is the most
expensive possible outcome of a request: with 512 as the limit, every request costs 512 tokens of generation.

What makes case 1 work is a chain, each link of which is now checked: the training text ends with `<turn|>` (a single token, id 106: `Gemma4TokenizerTest`), the model learns to produce it after an answer, `generate` stops on id 106 (passed explicitly, and in the base `generation_config`). `SFTTrainer` also appends the tokenizer's `eos_token` if the text does not end with it (`<eos>` for the `google/` tokenizer), which also stops generation. Check a model locally:

```python
print(model.generation_config)                  # eos_token_id: which ids stop generation
print(outputs[0][input_len:])                   # the generated ids: does one of the eos ids end the list?
```

How to tell from a request: **`output_tokens` close to `max_tokens`** means the reply ran to the limit instead of stopping;
a short reply has `output_tokens` far below it. The admin UI shows both for every request (trained-model dialog history).

## 4. Sampling and the returned text

- **Sampling.** `do_sample=True` with the request's `temperature` (default 0.7). gligabao sets no `top_k`, `top_p`,
  repetition penalty or seed, so generation uses whatever the base model's `generation_config` has for those (the base
  model's: `top_k` 64, `top_p` 0.95), and the same prompt gives different replies.
- **Decoding.** Only the newly generated ids are decoded (`tokenizer.decode(outputs[0][input_tokens:], skip_special_tokens=True)`),
  so the reply does not contain the echoed prompt. `extract_model_response` then strips the text, any echoed
  `<|turn>model` prefix and trailing `<turn|>` / `<eos>` markers (a no-op normally, since they are special tokens and
  are skipped). Before 0.46.1 the prompt was decoded with the reply and cut at the model marker, which with
  special tokens skipped found nothing, so replies came back starting with `user\n…\nmodel\n…`.
- **`input_tokens`** is the prompt as tokenized (special tokens such as BOS included); **`output_tokens`** the new tokens,
  the stop token included.

## 5. Where the time goes

A request, once the container is warm, is two phases (`transformers` `generate`):

1. **Prefill:** the prompt is processed in one pass. For a short prompt it is a fraction of a second.
2. **Decode:** the reply is produced **one token per forward pass**, each pass reading all the model's weights. Time grows
   linearly with `output_tokens`.

So, warm:

```
request time  ≈  routing overhead (a second or less)  +  prefill  +  output_tokens / tokens_per_second
```

`tokens_per_second` is what decides it. Roughly (an estimate, not a measurement of this system; *measure it*, see below):
HF `generate` with a bitsandbytes 4-bit model on an A10G decodes at about 10 to 30 tokens per second, because the 4-bit
weights are dequantized on the fly for every pass and `transformers` runs the loop in Python. At 15 tokens/s:

| `output_tokens` | decode time |
|---|---|
| 20 (a greeting) | about 1.5 s |
| 100 | about 7 s |
| 512 (the limit set in the admin UI) | about 35 s |
| 1024 (the default limit) | about 70 s |

That table is why a warm request of 30 to 50 seconds with *max tokens 512* is what a reply that runs to the limit looks
like. The same request with a short reply is seconds. Compare `output_tokens` with the time to tell which it was.

Cold requests add the container start and the model load on top (see [Cold and warm starts](../modal.md#cold-and-warm-starts)).

### Diagnosing a slow warm request

| You see | Meaning | What to try |
|---|---|---|
| `output_tokens` about `max_tokens` | The reply did not stop. | Check the stop chain (section 3): is the adapter trained before 0.46.1 (wrong end-of-turn marker, retrain)? Lower `max_tokens`. |
| `output_tokens` small, request still slow | Not decode time: queueing behind another request of the container, routing, or a cold container (`cold_start` true). | Look at `cold_start`; request again right after; check the container logs. |
| Time per token is high (seconds ÷ `output_tokens` far above about 0.07 s) | Slow decode. | A bf16 model instead of 4-bit (a 4B model fits an A10G), a static or compiled cache, or a serving engine such as vLLM. bf16 differs slightly from the 4-bit weights the adapter was trained on: compare outputs. |
| Same prompt with *max tokens 32* is fast | Length is the only cost. | See the first row. |

**What is not measured today:** the answer reports the total duration and the token counts but not the generation time
alone, so tokens per second has to be computed by hand (duration minus `load_seconds` and overhead, over `output_tokens`).
Recording the generation time in the sidecar's answer would make it a column.

## 6. Training, briefly

Both sizes train through Unsloth (`gemma4/model_train.py`): 4-bit base, LoRA r=16 / alpha 16 on `q/k/v/o_proj`, learning rate
2e-4, linear schedule with 10 warm-up steps, AdamW 8-bit, weight decay 0.01, bf16 when the GPU supports it, maximum
sequence length 2048, batch size 2 (4b) or 1 (12b) with gradient accumulation 4 or 8 (effective batch 8), gradient
checkpointing. The loss is over the whole formatted text. The job saves the adapter and the tokenizer to
`/models/trained/<job_id>`; nothing of the base model. Inference (`model_load_infer.py`) does not use Unsloth: it loads the
base with `transformers` + `bitsandbytes` and attaches the adapter with `peft`, which reads the same adapter files.
