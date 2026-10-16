"""Minimal gligabao sidecar: a FastAPI server listening on a local port."""
import sys
from typing import Optional
from contextlib import asynccontextmanager

import uvicorn
from fastapi import FastAPI, Request
from pydantic import ValidationError

from backends import Backend, get_backend
from routes import add_backend_routes
from sidecar_config import SidecarConfig


def get_config(request: Request) -> SidecarConfig:
    """The config the app was created with, for use in request handlers."""
    return request.app.state.config


def create_app(config: SidecarConfig, backend: Optional[Backend] = None) -> FastAPI:
    """Build the app. `config` is kept on `app.state.config`; pass it on to anything that needs config values.
    The API routes (routes.py) are answered by `backend`, by default the one `config` selects."""
    backend = backend or get_backend(config)

    @asynccontextmanager
    async def lifespan(_: FastAPI):
        yield
        await backend.aclose()

    app = FastAPI(title="gligabao sidecar", lifespan=lifespan)
    app.state.config = config
    app.state.backend = backend

    @app.get("/health")
    def health() -> dict:
        return {"status": "ok"}

    @app.get("/backends")
    async def backends() -> dict:
        """The backends available for training (today the one configured) with their status; see Backend.status."""
        return {"backends": [await backend.status()]}

    add_backend_routes(app, backend)
    return app


def main(config: SidecarConfig) -> None:
    uvicorn.run(create_app(config), host=config.host, port=config.port)


if __name__ == "__main__":
    try:
        cfg = SidecarConfig.from_env()
    except ValidationError as e:
        sys.exit(f"invalid sidecar config (Modal credentials are MODAL_TOKEN_ID and MODAL_TOKEN_SECRET):\n{e}")
    main(cfg)
