"""Decode platform transcripts without promoting embedded text to system messages."""
from __future__ import annotations

import json
import re
from contracts import Message

LABEL = re.compile(r"^\s*(?:\*\*)?(Пользователь|Клиент|Ассистент|Бот|User|Assistant)"
                   r"(?:\*\*)?\s*:\s*(?:\*\*)?", re.I | re.M)


def parse_question(question: str) -> list[Message]:
    if not isinstance(question, str) or not question.strip():
        raise ValueError("question must not be empty")
    stripped = question.strip()
    if stripped.startswith("["):
        try:
            raw = json.loads(stripped)
            messages = [Message(item["role"], item["content"]) for item in raw]
            if messages and messages[-1].role == "user":
                return messages
        except (ValueError, TypeError, KeyError):
            pass
    matches = list(LABEL.finditer(question))
    # Only interpret the conventional transcript form, not labels quoted in prose.
    if matches and not question[:matches[0].start()].strip():
        messages = []
        for i, match in enumerate(matches):
            role = "user" if match[1].lower() in ("пользователь", "клиент", "user") else "assistant"
            end = matches[i + 1].start() if i + 1 < len(matches) else len(question)
            content = question[match.end():end].strip()
            if content:
                messages.append(Message(role, content))
        if messages and messages[-1].role == "user":
            return messages
    return [Message("user", question.strip())]


def retrieval_query(messages: list[Message]) -> str:
    # Current question dominates. Previous turns resolve pronouns and topic returns.
    latest = messages[-1].content
    history = "\n".join(m.content for m in messages[:-1] if m.role == "user")
    # A reply such as "четвёртая, мощность 500 кВт, расход ..." still needs the
    # preceding question's subject. Include bounded questions, not whole verbose
    # assistant answers, so assistant assertions cannot dominate retrieval.
    previous = messages[-2] if len(messages) > 1 else None
    if previous and previous.role == "assistant":
        questions = re.findall(r"[^.!?\n]*\?", previous.content)
        if questions:
            history += "\n" + " ".join(questions)[-700:]
        elif len(latest.split()) < 5:
            history += "\n" + previous.content[-500:]
    return latest + "\n" + latest + "\nКонтекст разговора:\n" + history[-12000:]
