"""Version 1 integration contract shared by retrieval, ML and backend."""
from __future__ import annotations

from dataclasses import asdict, dataclass, field
from typing import Protocol, Sequence


@dataclass(frozen=True)
class Message:
    role: str
    content: str

    def __post_init__(self):
        if self.role not in ("user", "assistant") or not isinstance(self.content, str):
            raise ValueError("Only user/assistant text messages are accepted")
        if not self.content.strip():
            raise ValueError("Message must not be empty")


@dataclass(frozen=True)
class Fragment:
    document_id: str
    chunk_id: str
    text: str
    score: float = 0.0

    @property
    def source_id(self):
        return self.document_id + "#" + self.chunk_id


class Retriever(Protocol):
    def search(self, query: str, top_k: int = 12) -> Sequence[Fragment]: ...


@dataclass
class LeadState:
    session_id: str
    fields: dict = field(default_factory=dict)
    evidence: dict = field(default_factory=dict)
    requested: bool = False
    declined: bool = False


@dataclass
class ConsultationRequest:
    session_id: str
    messages: list[Message]
    lead: LeadState | None = None
    summary_so_far: dict = field(default_factory=dict)

    def __post_init__(self):
        if not isinstance(self.session_id, str) or not self.session_id.strip():
            raise ValueError("session_id is required")
        if not self.messages or self.messages[-1].role != "user":
            raise ValueError("The last message must be from the user")
        if len(self.messages) > 200 or sum(len(m.content) for m in self.messages) > 100000:
            raise ValueError("Session history exceeds supported limit; do not silently truncate")
        if self.lead and self.lead.session_id != self.session_id:
            raise ValueError("Lead belongs to a different session")
        if not isinstance(self.summary_so_far, dict):
            raise ValueError("summary_so_far must be an object")


@dataclass
class ConsultationResult:
    answer: str
    status: str
    sources: list[dict]
    elapsed_ms: int
    lead: LeadState
    lead_ready: bool
    summary: dict
    debug: dict = field(default_factory=dict)
    schema_version: str = "1.0"

    def to_dict(self):
        return asdict(self)
