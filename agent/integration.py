"""Backend adapter. The backend owns sessions, persistence and authentication."""
from __future__ import annotations

from pathlib import Path

from consultant import Consultant
from contracts import ConsultationRequest, LeadState, Message
from llm_client import LLMClient
from settings import Settings


def create_consultant(knowledge_zip: str | Path, *, retriever=None, settings=None, expert_snapshot=None, retrieval_mode="a2"):
    settings = settings or Settings.from_env()
    if retriever is None:
        from web_retrieval import WebRetriever
        retriever = WebRetriever(knowledge_zip, mode=retrieval_mode,
                                 context_chars=settings.context_chars, snapshot_path=expert_snapshot)
    return Consultant(LLMClient(settings), retriever, settings)


def respond_json(service: Consultant, payload: dict, *, deadline=None) -> dict:
    """Use in a thread pool from an async endpoint. Pass full active-session history."""
    if not isinstance(payload, dict):
        raise ValueError("Request must be an object")
    raw = payload.get("messages")
    if not isinstance(raw, list) or not all(isinstance(m, dict) for m in raw):
        raise ValueError("messages must be an array of objects")
    messages = [Message(m.get("role"), m.get("content")) for m in raw]
    old = payload.get("lead")
    if old is not None:
        if not isinstance(old, dict) or not isinstance(old.get("fields", {}), dict) or not isinstance(old.get("evidence", {}), dict):
            raise ValueError("Invalid lead object")
        if not all(isinstance(value, dict) for value in old.get("evidence", {}).values()):
            raise ValueError("Invalid lead evidence")
        if type(old.get("requested", False)) is not bool or type(old.get("declined", False)) is not bool:
            raise ValueError("Lead intent flags must be boolean")
        old = LeadState(old.get("session_id"), old.get("fields", {}), old.get("evidence", {}),
                        old.get("requested", False), old.get("declined", False))
    request = ConsultationRequest(payload.get("session_id"), messages, old, payload.get("summary_so_far") or {})
    return service.respond(request, deadline=deadline).to_dict()
