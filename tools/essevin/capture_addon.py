"""essevin registration capture addon.

Records every request/response that the driven Chrome window makes while the
registration flow runs, plus the browser-side fingerprint the page can observe.
The page observes two things the network layer cannot see: the real client
JavaScript values (navigator, WebGL, canvas, audio, fonts, screen) and every
setter/ic that a fingerprinting script touches. Those are exported through a
synthetic hook endpoint so they land in the same jsonl as the HTTP traffic.

Outputs (all under CAPTURE_DIR):
  raw.jsonl       one JSON object per event, ordered
  summary.json    post-run analysis: endpoints, leaks, fingerprint surface

Environment:
  CAPTURE_DIR   output directory (required)
  UPSTREAM      optional upstream proxy, e.g. http://127.0.0.1:10808
  INJECT_FP     1 = inject the page-side collector into HTML documents
"""

import json
import os
import re
import time
from urllib.parse import urlparse

from mitmproxy import http

HERE = os.path.dirname(os.path.abspath(__file__))
OUT_DIR = os.environ.get("CAPTURE_DIR", os.path.join(HERE, "run"))
INJECT = os.environ.get("INJECT_FP", "0") == "1"
RAW_PATH = os.path.join(OUT_DIR, "raw.jsonl")

# The synthetic endpoint the page-side collector posts its observation to.
HOOK_HOST = "essevin.capture"
HOOK_PATH = "/fp"

MAX_BODY = 262_144
# Values that must never be written to disk in the clear.
SECRET_KEYS = re.compile(
    r"password|passwd|pwd|secret|token|authorization|cookie|session|apikey|api_key",
    re.I,
)

COLLECTOR_PATH = os.path.join(HERE, "page_collector.js")


def load_collector():
    with open(COLLECTOR_PATH, encoding="utf-8") as fh:
        return fh.read()


def _jsonable(v, depth=0):
    """Recursively make provider values (bytes, tuples, multidicts) JSON-safe."""
    if depth > 8:
        return "<deep>"
    if v is None or isinstance(v, (bool, int, float, str)):
        return v
    if isinstance(v, (bytes, bytearray)):
        return bytes(v).decode("utf-8", "replace")
    if isinstance(v, dict):
        return {str(k): _jsonable(x, depth + 1) for k, x in v.items()}
    if isinstance(v, (list, tuple, set)):
        return [_jsonable(x, depth + 1) for x in v]
    try:
        return str(v)
    except Exception:
        return "<unrepr>"


def _redact(obj, depth=0):
    if depth > 6:
        return "<deep>"
    if isinstance(obj, dict):
        return {k: ("<redacted>" if SECRET_KEYS.search(str(k)) else _redact(v, depth + 1))
                for k, v in obj.items()}
    if isinstance(obj, list):
        return [_redact(v, depth + 1) for v in obj[:50]]
    return obj


def _decode_content(raw, headers):
    """Decode a content-encoded mitmproxy 'raw' body into bytes (or b'' on failure)."""
    if not raw:
        return b""
    enc = (headers.get("content-encoding") or "").lower().strip()
    try:
        if enc == "gzip" or enc == "x-gzip":
            import gzip
            return gzip.decompress(raw)
        if enc == "deflate":
            import zlib
            return zlib.decompress(raw)
        if enc == "br":
            import brotli
            return brotli.decompress(raw)
        if enc == "zstd":
            import zstandard
            return zstandard.ZstdDecompressor().decompressobj().decompress(raw)
    except Exception:
        return b""
    return raw


def _parse_body(raw, ctype="", headers=None):
    if not raw:
        return None
    if headers:
        raw = _decode_content(raw, headers) or raw
    if len(raw) > MAX_BODY:
        return {"<truncated>": len(raw)}
    try:
        text = raw.decode("utf-8", "replace")
    except Exception:
        return {"<binary>": len(raw)}
    if "json" in ctype.lower() or text.strip()[:1] in ("{", "["):
        try:
            return _redact(json.loads(text))
        except Exception:
            pass
    return text[:4000]


class Capture:
    def __init__(self):
        self.n = 0
        os.makedirs(OUT_DIR, exist_ok=True)
        self.fh = open(RAW_PATH, "a", encoding="utf-8", buffering=1)
        self.t0 = time.time()
        self.clients = set()

    def write(self, ev):
        self.n += 1
        ev["seq"] = self.n
        ev["ts"] = round(time.time() - self.t0, 3)
        self.fh.write(json.dumps(_jsonable(ev), ensure_ascii=False, default=str) + "\n")

    def client_connected(self, client):
        self.clients.add(client.peername)

    def requestheaders(self, flow: http.HTTPFlow):
        # Our venv lacks zstd; strip it so bodies arrive decodable for injection.
        ae = flow.request.headers.get("accept-encoding", "")
        if "zstd" in ae.lower() or "br" in ae.lower():
            kept = [p.strip() for p in ae.split(",") if "zstd" not in p.lower() and p.strip().lower() != "br"]
            if kept:
                flow.request.headers["accept-encoding"] = ", ".join(kept)
            else:
                del flow.request.headers["accept-encoding"]

    def request(self, flow: http.HTTPFlow):
        req = flow.request
        if req.host == HOOK_HOST:
            # Synthetic endpoint: the page posts its fingerprint observation here.
            try:
                payload = json.loads(req.get_text() or "{}")
            except Exception:
                payload = {"raw": req.get_text()[:4000]}
            self.write({"ev": "page", "origin": req.headers.get("origin"),
                        "referer": req.headers.get("referer"), "ua": req.headers.get("user-agent"),
                        "payload": payload})
            flow.response = http.Response.make(204, b"", {"Access-Control-Allow-Origin": "*"})
            return
        self.write({
            "ev": "req",
            "method": req.method,
            "scheme": req.scheme,
            "host": req.pretty_host,
            "path": req.path,
            "query": _jsonable(list(req.query.items(multi=True))) if req.query else [],
            "headers": [[k, v] for k, v in req.headers.items()],
            "body": _parse_body(req.raw_content, req.headers.get("content-type", ""), req.headers),
            "sni": getattr(flow.client_conn, "sni", None),
            "tls": getattr(flow.client_conn, "tls_established", False),
            "tls_version": getattr(flow.client_conn, "tls_version", None),
            "cipher": getattr(flow.client_conn, "cipher", None),
            "alpn": getattr(flow.client_conn, "alpn_proto_negotiated", None),
            "peername": getattr(flow.client_conn, "peername", None),
        })

    def response(self, flow: http.HTTPFlow):
        req, res = flow.request, flow.response
        if req.host == HOOK_HOST:
            return
        self.write({
            "ev": "res",
            "method": req.method,
            "host": req.pretty_host,
            "path": req.path,
            "status": res.status_code,
            "headers": [[k, v] for k, v in res.headers.items()],
            "body": _parse_body(res.raw_content, res.headers.get("content-type", ""), res.headers),
        })
        self._inject(res)

    def _inject(self, res):
        if not INJECT or "text/html" not in res.headers.get("content-type", ""):
            return
        try:
            self._inject_inner(res)
        except Exception as exc:
            import traceback
            self.write({"ev": "inject_error", "error": repr(exc),
                        "tb": traceback.format_exc()[-800:]})

    def _inject_inner(self, res):
        raw = _decode_content(res.raw_content, res.headers)
        if not raw:
            return
        html = raw.decode("utf-8", "replace")
        if "__EV_FP__" in html:
            return
        script = "<script>%s</script>" % load_collector()
        idx = html.lower().rfind("</head>")
        html = (html[:idx] + script + html[idx:]) if idx != -1 else (script + html)
        body = html.encode("utf-8")
        res.set_content(body)
        res.headers["content-length"] = str(len(body))
        res.headers.pop("content-encoding", None)
        self.write({"ev": "inject", "len": len(body)})


addons = [Capture()]
