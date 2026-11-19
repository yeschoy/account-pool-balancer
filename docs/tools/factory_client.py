#!/usr/bin/env python3
"""Factory Desktop 1.12.1 protocol client — 复刻真实客户端行为的参考实现.

依据 docs/factory-desktop-protocol.md 还原。用途：给反代/兼容层做参照实现与联调。

用法:
    export FACTORY_TOKEN='<Bearer token>'
    export FACTORY_ORG_ID='<org id>'          # 可选
    python3 factory_client.py identity        # 生成本地身份三元组
    python3 factory_client.py whoami
    python3 factory_client.py anthropic "hello"
    python3 factory_client.py openai "hello"
    python3 factory_client.py telemetry       # 发一条符合结构的事件

不写入任何凭证到磁盘；token 只从环境变量读。
"""
from __future__ import annotations

import base64
import json
import os
import secrets
import sys
import time
import uuid

API_BASE = os.environ.get("FACTORY_API_BASE", "https://api.factory.ai")
TELEMETRY_BASE = os.environ.get("FACTORY_TELEMETRY_BASE", "https://telemetry.factory.ai")

# ---- 鉴权 = WorkOS User Management（docs §2.4.1）----
WORKOS_BASE = os.environ.get("FACTORY_WORKOS_BASE", "https://api.workos.com/user_management")
WORKOS_CLIENT_PROD = "client_01HNM792M5G5G1A2THWPXKFMXB"
WORKOS_CLIENT_NONPROD = "client_01HNM7927XNSKCJ4982Z5J3FFZ"

# ---- 与 SDK 1.340.0 对齐的常量（docs §2.1）----
SDK_VERSION = "1.340.0"
H_CLIENT = "X-Factory-Client"
H_SURFACE = "X-Factory-Request-Surface"
H_CLIENT_VERSION = "X-Client-Version"
H_ORG = "X-Factory-Org-Id"
H_WHOAMI_EXT = "X-Factory-Whoami-Extended"

CLIENT_WEB_DESKTOP = "web-desktop"
SURFACE_DESKTOP = "desktop"

# ---- 网关路径（docs §3.1）----
PATH = {
    "anthropic": "/api/llm/a/v1/messages",
    "anthropic_native": "/api/llm/v1/messages",
    "openai_responses": "/api/llm/o/v1/responses",
    "openai_chat": "/api/llm/o/v1/chat/completions",
    "gemini": "/api/llm/g/v1/generate",
    "whoami": "/api/auth/whoami",
    "cli_whoami": "/api/cli/whoami",
    "config": "/api/auth/config",
    "refresh": "/api/auth/refresh",
    "usage": "/api/organization/usage-limits",
}

try:
    import requests
except ImportError:  # 允许无依赖时只跑 identity
    requests = None


# --------------------------------------------------------------------------
# 身份：与 SDK 的 getComputerIdentity / generateHostId 等价语义（docs §5.2）
# --------------------------------------------------------------------------
def gen_host_id() -> str:
    """hostId = UUIDv4（SDK: generateHostId = crypto.randomUUID）"""
    return str(uuid.uuid4())


def gen_installation_id() -> str:
    """droid-installation-id = UUIDv4 文本"""
    return str(uuid.uuid4())


def gen_computer_identity() -> dict:
    """计算机身份 = X25519 keypair + keyId，本地生成、明文 base64 落盘。

    与 SDK 一致：schemaVersion=1, algorithm="X25519"。
    没有 cryptography 时退化为占位（不参与真实握手，仅展示结构）。
    """
    try:
        from cryptography.hazmat.primitives.asymmetric import x25519
        from cryptography.hazmat.primitives import serialization

        priv = x25519.X25519PrivateKey.generate()
        priv_raw = priv.private_bytes(
            encoding=serialization.Encoding.Raw,
            format=serialization.PrivateFormat.Raw,
            encryption_algorithm=serialization.NoEncryption(),
        )
        pub_raw = priv.public_key().public_bytes(
            encoding=serialization.Encoding.Raw,
            format=serialization.PublicFormat.Raw,
        )
    except ImportError:
        priv_raw = secrets.token_bytes(32)
        pub_raw = secrets.token_bytes(32)

    return {
        "schemaVersion": 1,
        "publicKey": base64.b64encode(pub_raw).decode(),
        "privateKey": base64.b64encode(priv_raw).decode(),
        "keyId": base64.b64encode(secrets.token_bytes(8)).decode().rstrip("="),
        "algorithm": "X25519",
    }


def identity(bundle: bool = True) -> dict:
    """完整身份三元组，对应三个独立 UUID + 一个 keypair（docs §5.1）。"""
    out = {
        "hostId": gen_host_id(),
        "computerId": str(uuid.uuid4()),
        "droidInstallationId": gen_installation_id(),
    }
    if bundle:
        out["computerIdentity"] = gen_computer_identity()
    return out


# --------------------------------------------------------------------------
# 请求构造：对齐 docs §2.3 的合并顺序与 §6 的检查表
# --------------------------------------------------------------------------
def traceparent() -> str:
    """W3C traceparent: 00-<32hex>-<16hex>-01"""
    return f"00-{secrets.token_hex(16)}-{secrets.token_hex(8)}-01"


def default_headers(token: str | None = None, org_id: str | None = None,
                    version: str | None = None) -> dict:
    """复刻真客户端的默认头集合（保持原始大小写）。"""
    h = {
        "Content-Type": "application/json",
        H_CLIENT: CLIENT_WEB_DESKTOP,
        H_SURFACE: SURFACE_DESKTOP,
        "traceparent": traceparent(),
    }
    if version:
        h[H_CLIENT_VERSION] = version
    if token:
        h["Authorization"] = f"Bearer {token}"
    if org_id:
        h[H_ORG] = org_id
    return h


def anthropic_body(prompt: str, model: str = "claude-sonnet-4-5-20250929",
                   effort: str | None = "high", stream: bool = True) -> dict:
    """Factory 扩展：thinking.type=adaptive + output_config.effort（docs §3.2）。

    effort ∈ {low, medium, high, xhigh, max}
    """
    body: dict = {
        "model": model,
        "max_tokens": 1024,
        "messages": [{"role": "user", "content": prompt}],
        "stream": stream,
    }
    if effort:
        body["thinking"] = {"type": "adaptive"}
        body["output_config"] = {"effort": effort}
    return body


def openai_body(prompt: str, model: str = "gpt-5", stream: bool = True) -> dict:
    return {
        "model": model,
        "input": prompt,
        "stream": stream,
    }


# --------------------------------------------------------------------------
# 传输
# --------------------------------------------------------------------------
def _require_requests():
    if requests is None:
        sys.exit("需要 requests: python3 -m pip install requests")


def call(kind: str, body: dict, token: str | None = None, org_id: str | None = None,
         version: str | None = None, extra_headers: dict | None = None) -> int:
    """发一次推理请求；返回 HTTP 状态码，流式内容打到 stdout。"""
    _require_requests()
    url = API_BASE + PATH[kind]
    headers = default_headers(token, org_id, version)
    if extra_headers:
        headers.update(extra_headers)

    print(f"--> POST {url}", file=sys.stderr)
    print(json.dumps({k: v for k, v in headers.items()
                      if k.lower() not in ("authorization",)}, indent=1), file=sys.stderr)

    with requests.post(url, headers=headers, data=json.dumps(body),
                       stream=body.get("stream", False), timeout=120) as r:
        print(f"<-- {r.status_code} {r.headers.get('content-type', '')}", file=sys.stderr)
        if r.status_code >= 400:
            print(r.text[:2000], file=sys.stderr)
            return r.status_code
        if body.get("stream"):
            # SSE：逐行打印原始帧，便于对照协议
            for line in r.iter_lines(decode_unicode=True):
                if line:
                    print(line)
        else:
            print(json.dumps(r.json(), indent=2, ensure_ascii=False))
        return r.status_code


def whoami(token: str, org_id: str | None, extended: bool = True) -> int:
    _require_requests()
    headers = default_headers(token, org_id)
    if extended:
        headers[H_WHOAMI_EXT] = "true"
    r = requests.get(API_BASE + PATH["cli_whoami"], headers=headers, timeout=30)
    print(f"<-- {r.status_code}", file=sys.stderr)
    try:
        print(json.dumps(r.json(), indent=2, ensure_ascii=False))
    except ValueError:
        print(r.text[:2000])
    return r.status_code


def refresh_token(refresh_token_value: str, organization_id: str | None = None,
                  nonprod: bool = False) -> dict:
    """WorkOS token 刷新（docs §2.4.1）。

    POST {WORKOS_BASE}/authenticate
      grant_type=refresh_token & refresh_token=... & client_id=... [& organization_id=...]
    → { access_token, refresh_token }
    """
    _require_requests()
    client_id = WORKOS_CLIENT_NONPROD if nonprod else WORKOS_CLIENT_PROD
    form = {
        "grant_type": "refresh_token",
        "refresh_token": refresh_token_value,
        "client_id": client_id,
    }
    if organization_id:
        form["organization_id"] = organization_id
    r = requests.post(WORKOS_BASE + "/authenticate", data=form, timeout=30)
    if r.status_code >= 400:
        raise RuntimeError(f"WorkOS refresh failed {r.status_code}: {r.text[:500]}")
    body = r.json()
    # X-Factory-Org-Id 直接来自 active_organization_id
    body["_org_id_for_header"] = body.get("active_organization_id")
    return body


def send_telemetry(token: str | None, org_id: str | None) -> int:
    """构造一条结构合规的 user-message 事件（docs §4.1 / §4.2）。"""
    _require_requests()
    attrs = {
        "clientTimestampMs": int(time.time() * 1000),
        "clientPath": "/",
        "isClientMetric": False,
        "isClientLog": False,
        "clientPlatform": "desktop",
        "tags": {
            "clientType": "web-desktop",
            "environment": "production",
            "traceId": secrets.token_hex(16),
            "spanId": secrets.token_hex(8),
        },
    }
    payload = {"events": [{
        "type": "EVENT",
        "name": "user-message",
        "kind": "user-message",
        "messageId": str(uuid.uuid4()),
        "text": "hello from protocol client",
        "attributes": attrs,
    }]}
    headers = {
        "Content-Type": "application/json",
        H_CLIENT: "web-desktop",
    }
    if token:
        headers["Authorization"] = f"Bearer {token}"
    if org_id:
        headers[H_ORG] = org_id
    r = requests.post(TELEMETRY_BASE + "/api/ingest", headers=headers,
                      data=json.dumps(payload), timeout=30)
    print(f"<-- {r.status_code} {r.text[:300]}", file=sys.stderr)
    return r.status_code


# --------------------------------------------------------------------------
def main(argv: list[str]) -> int:
    token = os.environ.get("FACTORY_TOKEN")
    org_id = os.environ.get("FACTORY_ORG_ID")
    version = os.environ.get("FACTORY_CLIENT_VERSION")

    cmd = argv[1] if len(argv) > 1 else "help"
    prompt = argv[2] if len(argv) > 2 else "Say hi in one short sentence."

    if cmd == "identity":
        print(json.dumps(identity(), indent=2))
        return 0
    if cmd == "headers":
        # 只打印头集合（脱敏），用于核对反代头部
        h = default_headers(token, org_id, version)
        if "Authorization" in h:
            h["Authorization"] = "Bearer <redacted>"
        print(json.dumps(h, indent=2))
        return 0
    if cmd == "whoami":
        if not token:
            sys.exit("需要 FACTORY_TOKEN")
        return whoami(token, org_id)
    if cmd == "anthropic":
        return call("anthropic", anthropic_body(prompt), token, org_id, version)
    if cmd == "openai":
        return call("openai_responses", openai_body(prompt), token, org_id, version)
    if cmd == "refresh":
        rt = os.environ.get("FACTORY_REFRESH_TOKEN")
        if not rt:
            sys.exit("需要 FACTORY_REFRESH_TOKEN")
        out = refresh_token(rt, org_id, nonprod=(os.environ.get("FACTORY_ENV") != "production"))
        # 不打印明文 token
        print(json.dumps({k: ("<redacted>" if "token" in k else v) for k, v in out.items()}, indent=2))
        return 0
    if cmd == "telemetry":
        return send_telemetry(token, org_id)

    print(__doc__)
    return 1


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
