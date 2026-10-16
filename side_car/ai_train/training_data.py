"""Local (client-side) locations of training datasets."""
import os

modd_str = os.path.abspath(os.path.dirname(__file__))  # module dir


def get_local_paths() -> dict:
    return {
        "gemma4_tool_calling_exp1": {
            "path_str": f"{modd_str}/finetune_data/gemma4_tool_calling_news_finance_v1.jsonl",
        },
    }
