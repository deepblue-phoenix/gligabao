"""The sidecar's configuration: one pydantic struct, loaded once at startup and passed to everything that needs it."""
import os
from typing import Mapping, Optional

from pydantic import BaseModel, ConfigDict, Field, SecretStr, model_validator


class SidecarConfig(BaseModel):
    """All config values of the sidecar. Immutable; build it with `from_env()` and hand it around."""
    model_config = ConfigDict(frozen=True)

    host: str = Field("127.0.0.1", min_length=1)    # SIDECAR_HOST: interface to listen on
    port: int = Field(8787, ge=1, le=65535)         # SIDECAR_PORT: port to listen on

    backend: str = Field("modal", min_length=1)     # SIDECAR_BACKEND: what answers the API ("modal")

    # Modal backend. Credentials (MODAL_TOKEN_ID / MODAL_TOKEN_SECRET) are required when backend is "modal", so a
    # sidecar without them fails at startup instead of at the first training call. SecretStr keeps the values out
    # of repr()/logs; use .get_secret_value().
    modal_token_id: Optional[SecretStr] = None
    modal_token_secret: Optional[SecretStr] = None
    modal_app_name: str = Field("gligabao-train", min_length=1)  # SIDECAR_APP_NAME: the deployed Modal app
    modal_web_url: Optional[str] = None             # SIDECAR_MODAL_WEB_URL: skip the lookup, use this URL

    @model_validator(mode="after")
    def _check_backend(self) -> "SidecarConfig":
        if self.backend not in ("modal",):
            raise ValueError(f"unknown backend {self.backend!r} (SIDECAR_BACKEND): must be 'modal'")
        if self.backend == "modal":
            for name, var in (("modal_token_id", "MODAL_TOKEN_ID"), ("modal_token_secret", "MODAL_TOKEN_SECRET")):
                v = getattr(self, name)
                if v is None or not v.get_secret_value():
                    raise ValueError(f"{var} is required by the modal backend")
        return self

    @classmethod
    def from_env(cls, env: Optional[Mapping[str, str]] = None) -> "SidecarConfig":
        """Read the SIDECAR_* and MODAL_TOKEN_* variables (unset SIDECAR_* ones keep the defaults).
        Raises pydantic.ValidationError if a value is invalid or a Modal credential is missing or empty."""
        env = os.environ if env is None else env
        values = {}
        for field, var in (("host", "SIDECAR_HOST"), ("port", "SIDECAR_PORT"), ("backend", "SIDECAR_BACKEND"),
                           ("modal_app_name", "SIDECAR_APP_NAME"), ("modal_web_url", "SIDECAR_MODAL_WEB_URL"),
                           ("modal_token_id", "MODAL_TOKEN_ID"), ("modal_token_secret", "MODAL_TOKEN_SECRET")):
            if var in env:
                values[field] = env[var]
        return cls(**values)
