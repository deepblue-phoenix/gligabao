"""Pydantic models for the HTTP API. Provider neutral."""
import base64
from datetime import datetime, timezone
from typing import Optional

from pydantic import BaseModel, Field, field_validator


class TrainingDataset(BaseModel):
    """A training dataset (a .jsonl file on the data volume).

    Sent by golang to POST /datasets/upload (`filename` + `content`) and returned by GET /datasets/list
    (`content` is never serialized).
    """
    filename: str = Field(min_length=1)
    content: Optional[str] = Field(default=None, exclude=True)  # base64 or plain jsonl; upload only
    size_bytes: Optional[int] = None
    uploaded_at: Optional[datetime] = None
    modified_at: Optional[datetime] = None

    @field_validator("uploaded_at", "modified_at")
    @classmethod
    def _utc(cls, v: Optional[datetime]) -> Optional[datetime]:
        """Naive timestamps (incl. old metadata files) are local time; emit tz-aware so golang can parse them."""
        return v.astimezone(timezone.utc) if v is not None else None

    def payload(self) -> bytes:
        """Raw bytes of `content`: base64-decoded if it is valid base64, else the utf-8 string."""
        content = self.content or ""
        try:
            return base64.b64decode(content, validate=True)
        except Exception:  # not base64: treat as a plain utf-8 jsonl string
            return content.encode("utf-8")


class TrainingJob(BaseModel):
    """A training job started on the sidecar.

    Returned by POST /training/job_start; golang decodes it (internal/sidecar: TrainingJob) and records it in its
    `gb_training_job_runs` table.
    """
    job_id: str = Field(min_length=1)       # the sidecar's id of the job: names its progress file (GET /training/job_events)
    provider_job_id: str = ""               # the compute provider's id of the spawned call (for the provider's dashboard)
    status: str = "started"
    model_size: str
    num_train_steps: int = Field(gt=0)
    dataset: str = ""                       # file name on the data volume the job trains on ("" = the default dataset)
    started_at: datetime = Field(default_factory=lambda: datetime.now(timezone.utc))

    @field_validator("started_at")
    @classmethod
    def _utc(cls, v: datetime) -> datetime:
        """Naive timestamps are local time; emit tz-aware so golang can parse them."""
        return v.astimezone(timezone.utc)
