"""Validated configuration. No model name, service address or key is hardcoded."""
from dataclasses import dataclass, field
import math
import os
import warnings
from urllib.parse import urlsplit


class ConfigurationError(ValueError):
    pass


def env_first(*names, default="", environ=None):
    env = os.environ if environ is None else environ
    for name in names:
        value = env.get(name, "").strip()
        if value and value != name:
            return value
    return default


def number(name, default, lower, upper, kind=int, environ=None):
    raw = env_first(name, default=str(default), environ=environ)
    try:
        value = kind(raw)
    except (ValueError, TypeError):
        raise ConfigurationError(f"{name}: expected {kind.__name__}") from None
    if not math.isfinite(value) or not lower <= value <= upper:
        raise ConfigurationError(f"{name}: expected value in [{lower}, {upper}]")
    return value


def platform_number(name, default, lower, upper=None, kind=int, environ=None):
    """Platform options must not abort the entire batch before submission.csv.

    Workers/retrieval are bounded by local resource limits. A positive MAX_TOKENS
    is forwarded as supplied: the regulation does not impose our old 128..16384 range.
    Like the organizer's sample, malformed optional settings use a safe default.
    """
    raw = env_first(name, default=str(default), environ=environ)
    try:
        value = kind(raw)
        if not math.isfinite(value) or (upper is None and value < lower):
            raise ValueError()
    except (ValueError, TypeError, OverflowError):
        warnings.warn(f"{name}: invalid optional value; using default", RuntimeWarning, stacklevel=2)
        return default
    bounded = max(lower, value) if upper is None else max(lower, min(upper, value))
    if bounded != value:
        warnings.warn(f"{name}: limited to supported resource range", RuntimeWarning, stacklevel=2)
    return bounded


@dataclass(frozen=True)
class Settings:
    base_url: str
    api_key: str = field(repr=False)
    model: str
    temperature: float = 0.0
    max_tokens: int = 2048
    workers: int = 3
    timeout: float = 90.0
    attempts: int = 3
    top_k: int = 12
    context_chars: int = 32000
    runtime_seconds: float = 1120.0

    @classmethod
    def from_env(cls, environ=None):
        def get(*keys, default=""):
            return env_first(*keys, default=default, environ=environ)
        def num(key, default, lo, hi, kind=int):
            return number(key, default, lo, hi, kind, environ)
        def option(key, default, lo, hi=None, kind=int):
            return platform_number(key, default, lo, hi, kind, environ)
        base = get("LLM_BASE_URL", "OPENAI_BASE_URL", "BASE_URL").rstrip("/")
        key = get("LLM_API_KEY", "OPENAI_API_KEY")
        model = get("LLM_MODEL", "OPENAI_MODEL", "GEN_MODEL")
        if not base or not key or not model:
            missing = [label for label, value in (
                ("LLM_BASE_URL/OPENAI_BASE_URL/BASE_URL", base),
                ("LLM_API_KEY/OPENAI_API_KEY", key),
                ("LLM_MODEL/OPENAI_MODEL/GEN_MODEL", model)) if not value]
            raise ConfigurationError("Missing configuration: " + ", ".join(missing))
        parts = urlsplit(base)
        if (parts.scheme not in ("http", "https") or not parts.hostname
                or parts.username or parts.password or parts.query or parts.fragment):
            raise ConfigurationError("LLM_BASE_URL: expected HTTP(S) URL without credentials/query")
        if any(c in key for c in "\r\n"):
            raise ConfigurationError("LLM_API_KEY: invalid header characters")
        return cls(base, key, model,
                   option("TEMPERATURE", 0.0, 0.0, 2.0, float),
                   option("MAX_TOKENS", 2048, 1),
                   option("MAX_WORKERS", 3, 1, 8),
                   num("LLM_TIMEOUT", 90.0, 1.0, 180.0, float),
                   num("LLM_ATTEMPTS", 3, 1, 4),
                   option("TOP_K", 12, 1, 40),
                   num("CONTEXT_CHARS", 32000, 4000, 90000),
                   num("RUN_BUDGET_SECONDS", 1120.0, 1.0, 1140.0, float))
