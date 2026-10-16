"""Minimal gligabao sidecar: a FastAPI server listening on a local port."""
import os

import uvicorn
from fastapi import FastAPI

app = FastAPI(title="gligabao sidecar")


@app.get("/health")
def health() -> dict:
    return {"status": "ok"}


if __name__ == "__main__":
    uvicorn.run(
        app,
        host=os.environ.get("SIDECAR_HOST", "127.0.0.1"),
        port=int(os.environ.get("SIDECAR_PORT", "8787")),
    )
