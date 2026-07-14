"""Small deterministic OpenAI-compatible fixture for the local Compose stack."""

from __future__ import annotations

import json
import os
import ssl
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


class Handler(BaseHTTPRequestHandler):
    server_version = "llmtw-provider-mock/1"

    def log_message(self, _format: str, *_args: object) -> None:
        # Prompts and outputs must never be written to fixture logs either.
        return

    def _json(self, status: int, payload: dict[str, object]) -> None:
        encoded = json.dumps(payload, separators=(",", ":")).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

    def do_GET(self) -> None:  # noqa: N802 - stdlib handler API
        if self.path == "/health":
            self._json(200, {"status": "ok"})
            return
        self._json(404, {"error": {"type": "not_found"}})

    def do_POST(self) -> None:  # noqa: N802 - stdlib handler API
        if self.path != "/v1/chat/completions":
            self._json(404, {"error": {"type": "not_found"}})
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
        except ValueError:
            length = -1
        if length < 0 or length > 1 << 20:
            self._json(413, {"error": {"type": "request_too_large"}})
            return
        # Parse only to enforce valid JSON and keep this fixture aligned with
        # the request boundary. No caller content is copied into the response.
        try:
            json.loads(self.rfile.read(length))
        except (json.JSONDecodeError, UnicodeDecodeError):
            self._json(400, {"error": {"type": "invalid_json"}})
            return
        now = int(time.time())
        self._json(
            200,
            {
                "id": "mock-response-1",
                "object": "chat.completion",
                "created": now,
                "model": os.environ.get("MOCK_MODEL", "demo-model"),
                "choices": [
                    {
                        "index": 0,
                        "message": {"role": "assistant", "content": "fixture response"},
                        "finish_reason": "stop",
                    }
                ],
                "usage": {"prompt_tokens": 1, "completion_tokens": 2, "total_tokens": 3},
            },
        )


if __name__ == "__main__":
    server = ThreadingHTTPServer(("0.0.0.0", 8081), Handler)
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain("/srv/cert.pem", "/srv/key.pem")
    server.socket = context.wrap_socket(server.socket, server_side=True)
    server.serve_forever()
