"""Minimal gligabao sidecar: a FastAPI server listening on a local port."""
import sys

import uvicorn
from fastapi import FastAPI, Request
from pydantic import ValidationError

from sidecar_config import SidecarConfig


def get_config(request: Request) -> SidecarConfig:
    """The config the app was created with, for use in request handlers."""
    return request.app.state.config


def create_app(config: SidecarConfig) -> FastAPI:
    """Build the app. `config` is kept on `app.state.config`; pass it on to anything that needs config values."""
    app = FastAPI(title="gligabao sidecar")
    app.state.config = config

    @app.get("/health")
    def health() -> dict:
        return {"status": "ok"}

    return app


def main(config: SidecarConfig) -> None:
    uvicorn.run(create_app(config), host=config.host, port=config.port)


if __name__ == "__main__":
    try:
        cfg = SidecarConfig.from_env()
    except ValidationError as e:
        sys.exit(f"invalid sidecar config (Modal credentials are MODAL_TOKEN_ID and MODAL_TOKEN_SECRET):\n{e}")
    main(cfg)
