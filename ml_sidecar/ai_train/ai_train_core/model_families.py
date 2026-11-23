"""Registry of the supported model families. Pure Python, no heavy imports: safe to load anywhere.

Each family is a package next to this file (`gemma4/`, ...) that provides the same modules (see gemma4/__init__.py for the
contract). The family-neutral code (bootstrap.py, inference.py, config.py, the API) never imports a family directly: it
asks this registry for the module it needs, so a new family is one package plus one line in MODEL_FAMILIES.

Family modules are imported lazily, which keeps the training-only ones (model_train imports unsloth) out of the
inference image and out of the web container.
"""
import importlib
from types import ModuleType

MODEL_FAMILIES = ("gemma4",)
DEFAULT_MODEL_FAMILY = "gemma4"   # the family of a request that does not name one (the wire API has no such field yet)


def family_module(submodule: str, family: str = DEFAULT_MODEL_FAMILY) -> ModuleType:
    """The module `submodule` (e.g. "model_train", "prompt_format") of `family`. Raises ValueError for an unknown family."""
    if family not in MODEL_FAMILIES:
        raise ValueError(f"Invalid model_family '{family}'. Must be one of {list(MODEL_FAMILIES)}.")
    return importlib.import_module(f"{__package__}.{family}.{submodule}")


def model_sizes(family: str = DEFAULT_MODEL_FAMILY) -> tuple:
    return family_module("model_config", family).MODEL_SIZES


def get_model_config(model_size: str, family: str = DEFAULT_MODEL_FAMILY) -> dict:
    """Config of `model_size` in `family`. Raises ValueError for an unknown size."""
    return family_module("model_config", family).get_model_config(model_size)
