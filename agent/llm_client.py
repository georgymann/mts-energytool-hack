"""Small OpenAI-compatible client using only the Python standard library."""
import http.client
import json
import math
import random
import re
import socket
import ssl
import time
from urllib.parse import urlsplit

from settings import Settings


class ModelError(RuntimeError):
    """Safe, typed diagnostic: never contains a URL, key or server response body."""
    def __init__(self, code, *, status=None, retryable=False):
        self.code, self.status, self.retryable = code, status, retryable
        super().__init__(code + (f" (HTTP {status})" if status else ""))


class HTTPTransport:
    def post(self, url, headers, payload, timeout):
        parts = urlsplit(url)
        connection_type = (http.client.HTTPSConnection if parts.scheme == "https"
                           else http.client.HTTPConnection)
        conn = connection_type(parts.hostname, parts.port, timeout=timeout)
        end = time.monotonic() + timeout
        response = None
        try:
            conn.request("POST", parts.path or "/", body=payload, headers=headers)
            # getresponse() detaches conn.sock for Connection: close responses.
            # Keep the socket reference so the remaining deadline still applies
            # while reading their body, rather than resetting the full timeout.
            response_socket = conn.sock

            def apply_remaining_timeout():
                remaining = end - time.monotonic()
                if remaining <= 0:
                    raise TimeoutError()
                if response_socket:
                    response_socket.settimeout(remaining)

            apply_remaining_timeout()
            response = conn.getresponse()
            chunks, size = [], 0
            while not response.isclosed():
                apply_remaining_timeout()
                block = response.read1(65536)
                if not block:
                    break
                size += len(block)
                if size > 1_048_576:
                    raise ModelError("response_too_large")
                chunks.append(block)
            return response.status, dict(response.getheaders()), b"".join(chunks)
        finally:
            if response is not None:
                response.close()
            conn.close()


class LLMClient:
    def __init__(self, settings: Settings, transport=None, sleep=time.sleep):
        self.settings = settings
        self.transport = transport or HTTPTransport()
        self.sleep = sleep

    def complete(self, messages, *, deadline=None):
        cfg = self.settings
        body = json.dumps({"model": cfg.model, "messages": messages,
                           "temperature": cfg.temperature, "max_tokens": cfg.max_tokens},
                          ensure_ascii=False).encode("utf-8")
        headers = {"Authorization": "Bearer " + cfg.api_key,
                   "Content-Type": "application/json", "Accept": "application/json"}
        endpoint = cfg.base_url.rstrip("/")
        if not endpoint.endswith("/chat/completions"):
            endpoint += "/chat/completions"
        last = ModelError("unavailable", retryable=True)
        for attempt in range(cfg.attempts):
            remaining = cfg.timeout if deadline is None else deadline - time.monotonic()
            if remaining <= 0:
                raise ModelError("deadline_exceeded")
            retry_after = 0.0
            try:
                status, response_headers, raw = self.transport.post(
                    endpoint, headers, body, min(cfg.timeout, remaining))
                if status != 200:
                    try:
                        retry_after = float(next((v for k, v in response_headers.items()
                                                  if k.lower() == "retry-after"), "0"))
                    except (ValueError, TypeError):
                        retry_after = 0.0
                    if not math.isfinite(retry_after):
                        retry_after = 0.0
                    raise ModelError("api_error", status=status,
                                     retryable=status in (408, 429) or 500 <= status <= 599)
                try:
                    data = json.loads(raw)
                    choice = data["choices"][0]
                    content = choice["message"]["content"]
                    if choice.get("finish_reason") == "length":
                        # Repeating an identical token-limited request wastes the
                        # platform budget and does not repair a truncated JSON object.
                        raise ModelError("truncated_response", retryable=False)
                    if not isinstance(content, str):
                        raise ValueError()
                    # Qwen deployments may include visible thinking in content.
                    content = re.sub(r"<think>.*?</think>", "", content, flags=re.S | re.I).strip()
                    # Some deployments prepend the opening marker themselves,
                    # leaving only the closing tag in returned content.
                    if re.search(r"</think>", content, re.I):
                        content = re.split(r"</think>", content, flags=re.I)[-1].strip()
                    if not content or re.search(r"<think>", content, re.I):
                        raise ModelError("empty_response", retryable=True)
                    return content
                except (KeyError, IndexError, TypeError, ValueError, UnicodeError):
                    raise ModelError("invalid_response", retryable=True) from None
            except ModelError as exc:
                last = exc
            except (OSError, socket.timeout, ssl.SSLError, http.client.HTTPException):
                last = ModelError("transport_error", retryable=True)
            if not last.retryable or attempt + 1 >= cfg.attempts:
                raise last from None
            pause = max(min(max(retry_after, 0.0), 15.0),
                        min(0.6 * (2 ** attempt) + random.uniform(0, 0.2), 5.0))
            if deadline is not None and time.monotonic() + pause >= deadline:
                raise ModelError("deadline_exceeded")
            self.sleep(pause)
        raise last
