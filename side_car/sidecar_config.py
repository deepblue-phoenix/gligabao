"""The sidecar's configuration: one pydantic struct, loaded once at startup and passed to everything that needs it."""
import os
from typing import Mapping, Optional

from pydantic import BaseModel, ConfigDict, Field, SecretStr


class SidecarConfig(BaseModel):
    """All config values of the sidecar. Immutable; build it with `from_env()` and hand it around."""
    model_config = ConfigDict(frozen=True)

    host: str = Field("127.0.0.1", min_length=1)    # SIDECAR_HOST: interface to listen on
    port: int = Field(8787, ge=1, le=65535)         # SIDECAR_PORT: port to listen on

    # Modal API credentials (MODAL_TOKEN_ID / MODAL_TOKEN_SECRET): required, so a sidecar without them fails at startup
    # instead of at the first training call. SecretStr keeps the values out of repr()/logs; use .get_secret_value().
    modal_token_id: SecretStr = Field(min_length=1)
    modal_token_secret: SecretStr = Field(min_length=1)

    @classmethod
    def from_env(cls, env: Optional[Mapping[str, str]] = None) -> "SidecarConfig":
        """Read the SIDECAR_* and MODAL_TOKEN_* variables (unset SIDECAR_* ones keep the defaults).
        Raises pydantic.ValidationError if a value is invalid or a Modal credential is missing or empty."""
        env = os.environ if env is None else env
        values = {}
        for field, var in (("host", "SIDECAR_HOST"), ("port", "SIDECAR_PORT"),
                           ("modal_token_id", "MODAL_TOKEN_ID"), ("modal_token_secret", "MODAL_TOKEN_SECRET")):
            if var in env:
                values[field] = env[var]
        return cls(**values)
