"""Pydantic models for the HTTP API. Provider neutral."""
import base64
import binascii
from datetime import datetime, timezone
from pathlib import Path
from typing import Optional

from pydantic import BaseModel, Field, field_validator


class TrainingDataset(BaseModel):
    """A training dataset (a .jsonl file on the data volume), as it is passed around inside the sidecar.

    There is no endpoint for it: the Go server owns the datasets and hands one over inside POST /training/job_start
    (`dataset` = `filename`, `dataset_content` = `content`); the sidecar stores it on the data volume internally.
    `content` is never serialized.
    """
    filename: str = Field(min_length=1)
    content: Optional[str] = Field(default=None, exclude=True)  # base64 of the jsonl file
    size_bytes: Optional[int] = None
    uploaded_at: Optional[datetime] = None
    modified_at: Optional[datetime] = None

    @field_validator("uploaded_at", "modified_at")
    @classmethod
    def _utc(cls, v: Optional[datetime]) -> Optional[datetime]:
        """Naive timestamps are local time; emit tz-aware so golang can parse them."""
        return v.astimezone(timezone.utc) if v is not None else None

    def payload(self) -> bytes:
        """Raw bytes of `content`. Raises ValueError if it is missing, empty or not base64."""
        try:
            raw = base64.b64decode(self.content or "", validate=True)
        except (binascii.Error, ValueError):
            raise ValueError("dataset_content must be base64") from None
        if not raw:
            raise ValueError("dataset_content is empty")
        return raw

    @classmethod
    def from_path(cls, path: str, with_content: bool = False) -> "TrainingDataset":
        """Load a dataset file (e.g. from the data volume); `content` is filled (base64) only if with_content."""
        p = Path(path)
        st = p.stat()
        return cls(filename=p.name, size_bytes=st.st_size, modified_at=datetime.fromtimestamp(st.st_mtime),
                   content=base64.b64encode(p.read_bytes()).decode() if with_content else None)


class TrainingJob(BaseModel):
    """A training job started on the sidecar.

    Returned by POST /training/job_start; golang decodes it (internal/sidecar: TrainingJob) and records it in its
    `gb_training_job_runs` table.
    """
    job_id: str = Field(min_length=1)       # the sidecar's id of the job: names its event log (GET /training/job_events)
    provider_job_id: str = ""               # the compute provider's id of the spawned call (for the provider's dashboard)
    status: str = "started"
    model_size: str
    num_train_steps: int = Field(gt=0)
    dataset: str = ""                       # file name on the data volume the job trains on (<dataset name>.jsonl)
    started_at: datetime = Field(default_factory=lambda: datetime.now(timezone.utc))

    @field_validator("started_at")
    @classmethod
    def _utc(cls, v: datetime) -> datetime:
        """Naive timestamps are local time; emit tz-aware so golang can parse them."""
        return v.astimezone(timezone.utc)
