from __future__ import annotations

class GligabaoError(Exception):
    """Raised when the server returns an error or cannot be reached."""

    def __init__(self, message: str, status: int | None = None):
        super().__init__(message)
        self.status = status
