"""Everything specific to the Gemma-4 model family (4B and 12B). Nothing else in `ai_train_core` knows Gemma exists.

A model family is a package of this shape, registered in `..model_families`:

    model_config.py       MODEL_CONFIGS, MODEL_SIZES, get_model_config(size)          pure data, safe to import anywhere
    prompt_format.py      format_instruction, dataset_formatting_func, format_inference_prompt, bos_prefix,
                          stop_token_ids, extract_model_response                      no heavy imports
    model_train.py        run_finetuning(...) -> dict                                 training image only (imports unsloth)
    model_load_infer.py   load_finetuned_model_for_inference(...) -> (model, tokenizer, config, adapter_dir)
                                                                                      heavy imports inside the function
    model_load.py         family-private helper of model_train.py (Unsloth loading)

This file stays empty of imports on purpose: importing the package must never pull in torch/unsloth.
"""
