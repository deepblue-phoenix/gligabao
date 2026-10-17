# Gemma-4 Model Size Support

This training system supports both **Gemma-4 4B** and **Gemma-4 12B** models for fine-tuning.

## Model Comparison

| Feature | Gemma-4 4B | Gemma-4 12B |
|---------|------------|-------------|
| **Model Name** | `google/gemma-4-E4B-it` | `google/gemma-4-12B-it` |
| **Parameters** | 4 Billion | 12 Billion |
| **Trainable Params (LoRA r=16)** | ~60M (1.5%) | ~180M (1.5%) |
| **Recommended GPU** | A10G (24GB) | A100 (40GB/80GB) |
| **Modal Cost** | $1.10/hour | $4.10/hour |
| **Batch Size** | 2 | 1 |
| **Gradient Accumulation** | 4 steps | 8 steps |
| **Effective Batch Size** | 8 | 8 |
| **Training Speed** | ~1-2 min/100 steps | ~3-4 min/100 steps |

## Training Cost Estimates

### Gemma-4 4B on A10G ($1.10/hour)
- 100 steps: 5-10 min → **~$0.15**
- 500 steps: 25-40 min → **~$0.60**
- 1000 steps: 50-80 min → **~$1.20**

### Gemma-4 12B on A100 ($4.10/hour)
- 100 steps: 15-20 min → **~$0.50**
- 500 steps: 75-100 min → **~$2.40**
- 1000 steps: 150-200 min → **~$4.80**

## Usage Examples

### CLI

```bash
cd sidecar/ai_train
modal run app.py --action train --train-steps 500 --model-size 4b     # A10G
modal run app.py --action train --train-steps 500 --model-size 12b    # A100
modal run app.py --action generate --model-size 12b --prompt "..."
```

### HTTP API (after `modal serve app.py` / `modal deploy app.py`)

```bash
curl -X POST $URL/training/job_start -H "Content-Type: application/json" \
  -d '{"num_train_steps": 500, "model_size": "12b"}'
```

## When to Use Each Model

### Use 4B Model When:
- ✅ You want **lower cost** training
- ✅ You have a **smaller dataset** (<1000 examples)
- ✅ You need **faster iteration** during development
- ✅ Your task is **relatively simple** (tool calling, classification, basic Q&A)
- ✅ You want to **test quickly** before committing to larger training runs

### Use 12B Model When:
- ✅ You need **higher quality** outputs
- ✅ You have a **larger dataset** (>1000 examples)
- ✅ Your task requires **complex reasoning** or **longer context**
- ✅ You're training for **production deployment**
- ✅ You can afford the **3-4x higher cost**

## Technical Details

### Memory Requirements (4-bit Quantization)

| Model | Full Precision | 4-bit Quantized | LoRA Adapters | Training Total |
|-------|----------------|-----------------|---------------|----------------|
| 4B | ~8GB | ~2GB | ~0.5GB | ~2.5GB |
| 12B | ~24GB | ~6GB | ~1.5GB | ~7.5GB |

### GPU Selection Logic

Defaults come from `ai_train_core/model_config.py` (`default_gpu`): `4b` -> `a10g`, `12b` -> `a100`.
`SIDECAR_GPU` overrides both.

### Training stack

Both sizes train and serve through Unsloth, in one shared image (`requirements_train.txt`: unsloth 2026.10.x, torch 2.10+, CUDA 12.8). Code: `model_load.py`, `model_train.py`, `inference.py`.

A job saves only its LoRA adapter, in `/models/trained/<job_id>` of the models volume, as a new trained model (the base model stays in the Hugging Face cache volume and is shared). Inference picks the adapter with `model_path` and needs `model_size` to match the size it was trained for. See [Trained models](../sidecar.md#trained-models).

`google/gemma-4-12B-it` ships the newer `gemma4_unified` architecture, which Unsloth supports from 2026.10.x (earlier releases pinned `transformers<=5.5.0` and could not load it). If its output looks broken, see Unsloth issue [#6028](https://github.com/unslothai/unsloth/issues/6028).

### LoRA Configuration

Both models use rank 16, alpha 16, dropout 0 on the q/k/v/o attention projections, with gradient checkpointing.

## Recommended Workflow

1. **Start with 4B** for initial testing and dataset validation
2. Run **100-200 steps** to verify loss is decreasing
3. If results are good but need improvement, try:
   - More training steps on 4B (500-1000)
   - OR switch to 12B for higher quality
4. For **production**, use 12B with 500-1000+ steps

## Troubleshooting

### Out of Memory (OOM) Errors

**4B on A10G:**
- ✅ Should never OOM with batch_size=2
- If OOM occurs: reduce batch_size to 1, increase grad_accumulation to 8

**12B on A100:**
- ✅ Should fit with batch_size=1
- If OOM occurs: ensure using 4-bit quantization and gradient checkpointing
- Consider A100 80GB variant if available

### Training Too Slow

**4B:**
- Switch from A10G to A100 for ~2x speedup (but higher cost)

**12B:**
- Use A100 80GB for slightly faster training
- Consider reducing max_seq_length from 2048 to 1024

## Model Card References

- **4B Model:** [google/gemma-4-E4B-it](https://huggingface.co/google/gemma-4-E4B-it)
- **12B Model:** [google/gemma-4-12B-it](https://huggingface.co/google/gemma-4-12B-it)
- **Unsloth:** [unslothai/unsloth](https://github.com/unslothai/unsloth)
