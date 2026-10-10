"""Python client for the gligabao server."""

from .client import Client
from .errors import GligabaoError

__all__ = ["Client", "GligabaoError"]
__version__ = "0.1.0"
