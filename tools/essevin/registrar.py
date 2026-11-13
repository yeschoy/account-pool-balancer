#!/usr/bin/env python3
"""essevin.com registration client.

Protocol recovered from a live browser capture (see run/raw.jsonl):

    POST /api/v1/auth/send-verify-code  {"email", "lang"}
    POST /api/v1/auth/verify-code       {"email", "code"}
    POST /api/v1/auth/register          {"email", "password", "verify_code", "username"?, ...}
    POST /api/v1/api-keys               {"name", ...}        (browser session token)

Design rules this module follows:

* One identity per account. The User-Agent, Sec-CH-UA hints, Accept-Language,
  timezone in query strings and the egress IP are all chosen together from a
  single persisted profile. Nothing rotates between requests of one account,
  and nothing is shared between two accounts.
* The egress is the account's own sticky proxy, not the host IP. The proxy is
  used through an optional local upstream hop (sing-box), so the proxy
  provider's source-IP allowlist sees a stable permitted address.
* Secrets never reach the log. Passwords and session tokens are redacted.
"""

from __future__ import annotations

import base64
import json
import random
import re
import secrets
import socket
import ssl
import string
import time
from dataclasses import dataclass, field
from typing import Any, Callable, Iterable
from urllib.parse import urlencode, urlparse

# ---------------------------------------------------------------- constants

CONSOLE = "https://console.essevin.com"
API_BASE = CONSOLE + "/api/v1"

EP_SEND = API_BASE + "/auth/send-verify-code"
EP_VERIFY = API_BASE + "/auth/verify-code"
EP_REGISTER = API_BASE + "/auth/register"
EP_ME = API_BASE + "/users/me"
EP_KEYS = API_BASE + "/api-keys"
EP_SETTINGS = API_BASE + "/settings/public"
EP_PLANS = API_BASE + "/plans"
EP_PRICING = API_BASE + "/models/pricing/me"
EP_GROUPS = API_BASE + "/groups"
EP_PLANS_FULL = API_BASE + "/plans"

# Server-side rate limit observed on send-verify-code (per email, 60s).
SEND_COOLDOWN = 60

ACCEPT_LANGUAGE = "en-US,en;q=0.9"


class RegError(RuntimeError):
    """A failure with an error code the caller can branch on."""

    def __init__(self, code: str, message: str, *, status: int | None = None, body: Any = None):
        super().__init__(f"{code}: {message}")
        self.code = code
        self.message = message
        self.status = status
        self.body = body


# ---------------------------------------------------------------- identity

@dataclass(frozen=True)
class Identity:
    """One coherent desktop-browser identity.

    Chrome reduces its UA to major.0.0.0 and ships matching client hints; the
    three values below must stay consistent or the request looks self-made.
    """

    user_agent: str
    sec_ch: str
    platform: str            # Sec-CH-UA-Platform, quoted
    platform_version: str    # Sec-CH-UA-Platform-Version, quoted
    accept_language: str
    timezone: str            # IANA name, used in ?tz=
    browser_major: str

    @staticmethod
    def chrome(*, major: str = "157", platform: str = "macos") -> "Identity":
        tokens = {
            "macos": ("Macintosh; Intel Mac OS X 10_15_7", '"macOS"', '"15.7.0"'),
            "windows": ("Windows NT 10.0; Win64; x64", '"Windows"', '"19.0.0"'),
            "linux": ("X11; Linux x86_64", '"Linux"', '"6.8.0"'),
        }[platform]
        ua_tok, ch_platform, ch_platform_ver = tokens
        ua = (f"Mozilla/5.0 ({ua_tok}) AppleWebKit/537.36 "
              f"(KHTML, like Gecko) Chrome/{major}.0.0.0 Safari/537.36")
        # Brand list order follows Chrome's GREASE pattern for current builds.
        sec_ch = (f'"Not-A.Brand";v="99", "Google Chrome";v="{major}", '
                  f'"Chromium";v="{major}"')
        return Identity(
            user_agent=ua, sec_ch=sec_ch, platform=ch_platform,
            platform_version=ch_platform_ver, accept_language=ACCEPT_LANGUAGE,
            timezone="America/New_York", browser_major=major,
        )


class IdentityPool:
    """Deterministic, collision-light assignment of identities to accounts."""

    PLATFORMS = ("macos", "windows", "linux")
    MAJORS = ("156", "157")

    def __init__(self, seed: bytes | None = None):
        self._rng = random.Random(seed if seed is not None else secrets.randbits(64))

    def pick(self) -> Identity:
        return Identity.chrome(major=self._rng.choice(self.MAJORS),
                               platform=self._rng.choice(self.PLATFORMS))


# ---------------------------------------------------------------- proxy chain

@dataclass
class ProxyEndpoint:
    """A sticky upstream proxy, optionally reached through a local hop.

    `url` is the upstream, e.g. socks5://user:pass@host:port or
    http://user:pass@host:port. `upstream_http` is a local HTTP CONNECT proxy
    (e.g. sing-box on 127.0.0.1:10808) used to reach it when the upstream
    provider only permits its own allowlisted source IPs.
    """

    name: str
    host: str
    port: int
    scheme: str = "http"          # scheme the upstream speaks: http | socks5
    username: str = ""
    password: str = ""
    upstream_http: str | None = None   # http://127.0.0.1:10808

    @staticmethod
    def parse(name: str, url: str, upstream_http: str | None = None) -> "ProxyEndpoint":
        u = urlparse(url)
        return ProxyEndpoint(
            name=name, host=u.hostname or "", port=u.port or 0,
            scheme=(u.scheme or "http").lower(),
            username=u.username or "", password=u.password or "",
            upstream_http=upstream_http,
        )

    def describe(self) -> str:
        """Redacted form, safe for logs."""
        return f"{self.name} -> {self.scheme}://{self.host}:{self.port}" + (
            f" via {self.upstream_http}" if self.upstream_http else "")


def _read_until(sock: socket.socket, marker: bytes, limit: int = 65536) -> bytes:
    buf = b""
    while marker not in buf:
        chunk = sock.recv(4096)
        if not chunk:
            raise RegError("PROXY_CLOSED", "proxy closed before response header")
        buf += chunk
        if len(buf) > limit:
            raise RegError("PROXY_HEADER", "proxy response header too large")
    return buf


def open_tunnel(px: ProxyEndpoint, host: str, port: int, timeout: float = 25.0) -> socket.socket:
    """Open a raw socket to host:port through `px` (HTTP CONNECT or SOCKS5)."""
    if px.upstream_http:
        up = urlparse(px.upstream_http)
        sock = socket.create_connection((up.hostname, up.port or 8080), timeout=timeout)
        target = f"{px.host}:{px.port}"
        sock.sendall(f"CONNECT {target} HTTP/1.1\r\nHost: {target}\r\n\r\n".encode())
        head = _read_until(sock, b"\r\n\r\n")
        if b" 200 " not in head.split(b"\r\n")[0]:
            sock.close()
            raise RegError("PROXY_HOP", "local hop refused the tunnel")
        # Any bytes after the header belong to the next protocol.
        leftover = head.split(b"\r\n\r\n", 1)[1]
    else:
        sock = socket.create_connection((px.host, px.port), timeout=timeout)
        leftover = b""

    try:
        if px.scheme == "socks5":
            _socks5_handshake(sock, px, host, port, leftover)
        elif px.scheme in ("http", "https"):
            _http_connect(sock, px, host, port, leftover)
        else:
            raise RegError("PROXY_SCHEME", f"unsupported proxy scheme {px.scheme!r}")
    except Exception:
        sock.close()
        raise
    return sock


def _socks5_handshake(sock: socket.socket, px: ProxyEndpoint,
                      host: str, port: int, leftover: bytes) -> None:
    if leftover:
        # Should not happen on SOCKS5 greeting; treat as protocol error.
        raise RegError("PROXY_DIRTY", "unexpected bytes before SOCKS5 greeting")
    if px.username:
        sock.sendall(b"\x05\x02\x00\x02")
        resp = sock.recv(2)
        if len(resp) < 2 or resp[1] != 0x02:
            raise RegError("PROXY_AUTH", "SOCKS5 server does not accept username auth")
        u = px.username.encode()
        p = px.password.encode()
        sock.sendall(b"\x01" + bytes([len(u)]) + u + bytes([len(p)]) + p)
        st = sock.recv(2)
        if len(st) < 2 or st[1] != 0:
            raise RegError("PROXY_AUTH", "SOCKS5 username/password rejected")
    else:
        sock.sendall(b"\x05\x01\x00")
        resp = sock.recv(2)
        if len(resp) < 2 or resp[1] != 0x00:
            raise RegError("PROXY_AUTH", "SOCKS5 proxy requires authentication")

    hb = host.encode()
    sock.sendall(b"\x05\x01\x00\x03" + bytes([len(hb)]) + hb + port.to_bytes(2, "big"))
    rep = sock.recv(10)
    if len(rep) < 2 or rep[1] != 0:
        raise RegError("PROXY_CONNECT", f"SOCKS5 connect failed (rep={rep[1] if len(rep) > 1 else '?'})")


def _http_connect(sock: socket.socket, px: ProxyEndpoint,
                  host: str, port: int, leftover: bytes) -> None:
    target = f"{host}:{port}"
    lines = [f"CONNECT {target} HTTP/1.1", f"Host: {target}"]
    if px.username:
        token = base64.b64encode(f"{px.username}:{px.password}".encode()).decode()
        lines.append(f"Proxy-Authorization: Basic {token}")
    sock.sendall(("\r\n".join(lines) + "\r\n\r\n").encode())
    head = _read_until(sock, b"\r\n\r\n")
    status_line = head.split(b"\r\n")[0]
    if b" 200 " not in status_line:
        raise RegError("PROXY_CONNECT", f"HTTP CONNECT failed: {status_line!r}")


# ---------------------------------------------------------------- HTTP/1.1 client

class HttpsClient:
    """Minimal HTTP/1.1 + TLS client bound to one proxy tunnel per call.

    Using a fresh TLS connection per request keeps the TLS fingerprint stable
    (one handshake shape, no session-resumption variance) and avoids reusing
    a tunnel that an upstream may have half-closed.
    """

    def __init__(self, identity: Identity, proxy: ProxyEndpoint | None = None,
                 timeout: float = 40.0):
        self.identity = identity
        self.proxy = proxy
        self.timeout = timeout
        self._ctx = ssl.create_default_context()
        self._ctx.set_alpn_protocols(["http/1.1"])

    # -- transport -----------------------------------------------------
    def _connect(self, host: str) -> tuple[socket.socket, socket.socket]:
        if self.proxy:
            raw = open_tunnel(self.proxy, host, 443, timeout=self.timeout)
        else:
            raw = socket.create_connection((host, 443), timeout=self.timeout)
        tls = self._ctx.wrap_socket(raw, server_hostname=host)
        return raw, tls

    def request(self, method: str, url: str, *,
                json_body: Any | None = None,
                headers: dict[str, str] | None = None,
                bearer: str | None = None) -> tuple[int, dict[str, str], bytes]:
        u = urlparse(url)
        host = u.hostname or ""
        path = u.path or "/"
        if u.query:
            path += "?" + u.query

        hdrs = {
            "Host": host,
            "User-Agent": self.identity.user_agent,
            "Accept": "application/json, text/plain, */*",
            "Accept-Language": self.identity.accept_language,
            "Accept-Encoding": "gzip, deflate",
            "sec-ch-ua": self.identity.sec_ch,
            "sec-ch-ua-mobile": "?0",
            "sec-ch-ua-platform": self.identity.platform,
            "sec-ch-ua-platform-version": self.identity.platform_version,
            "Origin": CONSOLE,
            "Referer": CONSOLE + "/login?tab=register",
            "Sec-Fetch-Site": "same-origin",
            "Sec-Fetch-Mode": "cors",
            "Sec-Fetch-Dest": "empty",
            "Priority": "u=1, i",
        }
        body = b""
        if json_body is not None:
            body = json.dumps(json_body, separators=(",", ":")).encode()
            hdrs["Content-Type"] = "application/json"
            hdrs["Content-Length"] = str(len(body))
        if bearer:
            hdrs["Authorization"] = f"Bearer {bearer}"
        if headers:
            hdrs.update(headers)

        req = f"{method} {path} HTTP/1.1\r\n"
        req += "".join(f"{k}: {v}\r\n" for k, v in hdrs.items())
        req += "\r\n"

        raw, tls = self._connect(host)
        try:
            tls.sendall(req.encode() + body)
            head, payload = _recv_http_response(tls)
        finally:
            try:
                tls.close()
            finally:
                raw.close()

        status = int(head.split(b" ")[1])
        hmap = {}
        for line in head.split(b"\r\n")[1:]:
            if b":" in line:
                k, v = line.split(b":", 1)
                hmap[k.decode().lower()] = v.decode().strip()
        enc = hmap.get("content-encoding", "").lower()
        if enc == "gzip":
            import gzip
            payload = gzip.decompress(payload)
        elif enc == "deflate":
            import zlib
            payload = zlib.decompress(payload)
        return status, hmap, payload

    def get_json(self, url: str, *, bearer: str | None = None) -> Any:
        status, _, body = self.request("GET", url, bearer=bearer)
        return _unwrap(status, body)

    def post_json(self, url: str, payload: dict, *, bearer: str | None = None) -> Any:
        status, _, body = self.request("POST", url, json_body=payload, bearer=bearer)
        return _unwrap(status, body)


def _recv_http_response(sock: socket.socket) -> tuple[bytes, bytes]:
    buf = b""
    while b"\r\n\r\n" not in buf:
        chunk = sock.recv(65536)
        if not chunk:
            raise RegError("NET_CLOSED", "connection closed before response header")
        buf += chunk
    head, rest = buf.split(b"\r\n\r\n", 1)
    headers = {}
    for line in head.split(b"\r\n")[1:]:
        if b":" in line:
            k, v = line.split(b":", 1)
            headers[k.decode().lower()] = v.decode().strip()

    if headers.get("transfer-encoding", "").lower() == "chunked":
        body = b""
        data = rest
        while True:
            while b"\r\n" not in data:
                c = sock.recv(65536)
                if not c:
                    raise RegError("NET_CLOSED", "chunked body truncated")
                data += c
            size_line, data = data.split(b"\r\n", 1)
            size = int(size_line.split(b";")[0], 16)
            if size == 0:
                break
            while len(data) < size + 2:
                c = sock.recv(65536)
                if not c:
                    raise RegError("NET_CLOSED", "chunked body truncated")
                data += c
            body += data[:size]
            data = data[size + 2:]
        return head, body

    length = int(headers.get("content-length", "0") or 0)
    body = rest
    while len(body) < length:
        c = sock.recv(65536)
        if not c:
            break
        body += c
    return head, body[:length]


def _unwrap(status: int, body: bytes) -> Any:
    try:
        parsed = json.loads(body.decode("utf-8", "replace"))
    except Exception:
        parsed = None
    if status >= 400 or (isinstance(parsed, dict) and parsed.get("code") not in (0, None)):
        msg = ""
        code = None
        if isinstance(parsed, dict):
            msg = str(parsed.get("message") or parsed.get("error") or "")
            code = parsed.get("code")
        raise RegError(f"HTTP_{status}" if status >= 400 else f"API_{code}",
                       msg or "request failed", status=status, body=parsed)
    return parsed.get("data", parsed) if isinstance(parsed, dict) else parsed


# ---------------------------------------------------------------- egress check

def check_egress(px: ProxyEndpoint, timeout: float = 30.0) -> str:
    """Return the public IP this proxy presents to the internet."""
    sock = open_tunnel(px, "api.ipify.org", 80, timeout=timeout)
    try:
        sock.sendall(b"GET /?format=text HTTP/1.1\r\nHost: api.ipify.org\r\n"
                     b"Connection: close\r\n\r\n")
        head, body = _recv_http_response(sock)
    finally:
        sock.close()
    ip = body.strip().decode(errors="replace")
    if not re.fullmatch(r"[0-9a-fA-F:.]+", ip):
        raise RegError("EGRESS_CHECK", f"unexpected egress response: {ip[:80]!r}")
    return ip


# ---------------------------------------------------------------- mailbox

@dataclass
class MailMessage:
    subject: str
    body: str
    received: str = ""


class ICloudMailClient:
    """Reads an iCloud Hide-My-Email alias through its public share link.

    Share link shape:
        https://<host>/messages/<alias>?token=<token>
    and the API behind it:
        GET <host>/api/messages?alias_email=<alias>&token=<token>&range=latest
    """

    def __init__(self, share_url: str, timeout: float = 30.0):
        u = urlparse(share_url)
        self.base = f"{u.scheme}://{u.netloc}"
        m = re.search(r"/messages/([^/?#]+)", u.path)
        if not m:
            raise RegError("MAIL_URL", "share link has no /messages/<alias> segment")
        self.alias = m.group(1).replace("%40", "@")
        q = dict(p.split("=", 1) for p in (u.query or "").split("&") if "=" in p)
        self.token = q.get("token", "")
        if not self.token:
            raise RegError("MAIL_URL", "share link has no token")
        self.timeout = timeout

    def messages(self) -> list[MailMessage]:
        import urllib.request
        qs = urlencode({"alias_email": self.alias, "token": self.token, "range": "latest"})
        req = urllib.request.Request(f"{self.base}/api/messages?{qs}",
                                     headers={"Accept": "application/json"})
        with urllib.request.urlopen(req, timeout=self.timeout) as resp:
            data = json.loads(resp.read())
        if not data.get("ok"):
            raise RegError("MAIL_API", str(data.get("error") or "mail api error"))
        out = []
        for e in data.get("data", {}).get("emails", []) or []:
            out.append(MailMessage(
                subject=str(e.get("subject") or ""),
                body=str(e.get("text") or e.get("body") or e.get("html") or
                         e.get("preview") or e.get("snippet") or ""),
                received=str(e.get("receivedAt") or e.get("date") or e.get("createdAt") or ""),
            ))
        return out

    def wait_code(self, *, since: float, pattern: str, timeout: float = 180.0,
                  interval: float = 5.0,
                  log: Callable[[str], None] = lambda s: None) -> str:
        """Poll until a message received after `since` contains `pattern`.

        `pattern` is a regex with one capture group, or the literal code.
        """
        rx = re.compile(pattern)
        deadline = time.time() + timeout
        while time.time() < deadline:
            for msg in self.messages():
                text = msg.subject + "\n" + msg.body
                m = rx.search(text)
                if m:
                    code = m.group(1) if m.groups() else m.group(0)
                    log(f"mail: code found in {msg.subject[:60]!r}")
                    return code
            time.sleep(interval)
        raise RegError("MAIL_TIMEOUT", f"no verification mail for {self.alias} within {timeout}s")


# ---------------------------------------------------------------- registrar

@dataclass
class RegisterResult:
    email: str
    user_id: int | None = None
    token: str = ""
    api_key: str = ""
    api_key_id: int | None = None
    balance: Any = None
    identity: Identity | None = None
    egress_ip: str = ""
    proxy: str = ""
    keys: list[dict] = field(default_factory=list)
    warn: list[str] = field(default_factory=list)


def password_for(length: int = 16) -> str:
    """Random password that satisfies the >=8 rule and typical strength checks."""
    alphabet = string.ascii_letters + string.digits + "!@#$%^&*"
    while True:
        pw = "".join(secrets.choice(alphabet) for _ in range(length))
        if (any(c.islower() for c in pw) and any(c.isupper() for c in pw)
                and any(c.isdigit() for c in pw) and any(c in "!@#$%^&*" for c in pw)):
            return pw


class Registrar:
    def __init__(self, *, identity: Identity, proxy: ProxyEndpoint | None = None,
                 log: Callable[[str], None] = print):
        self.identity = identity
        self.proxy = proxy
        self.log = log
        self.http = HttpsClient(identity, proxy)

    # -- step helpers --------------------------------------------------
    def send_verify_code(self, email: str, lang: str = "en") -> None:
        self.log(f"send-verify-code -> {email}")
        self.http.post_json(EP_SEND, {"email": email, "lang": lang})

    def verify_code(self, email: str, code: str) -> None:
        self.log("verify-code")
        self.http.post_json(EP_VERIFY, {"email": email, "code": code})

    def register(self, email: str, password: str, code: str,
                 username: str | None = None) -> tuple[str, dict]:
        self.log("register")
        payload: dict[str, Any] = {"email": email, "password": password, "verify_code": code}
        if username:
            payload["username"] = username
        data = self.http.post_json(EP_REGISTER, payload)
        token = data.get("token") or ""
        user = data.get("user") or {}
        if not token:
            raise RegError("NO_TOKEN", "register succeeded but returned no session token")
        return token, user

    def me(self, token: str) -> dict:
        return self.http.get_json(EP_ME, bearer=token)

    def groups(self, token: str) -> list[dict]:
        """Model groups this account may bind keys to.

        /groups is empty until a subscription exists, while the free/trial plan
        already lists bindable groups in plans[].included_groups. Union both so
        a freshly registered account can still mint keys.
        """
        out: list[dict] = []
        try:
            data = self.http.get_json(EP_GROUPS + "?page=1&page_size=100", bearer=token)
            out.extend((data or {}).get("list", []) or [])
        except Exception:
            pass
        if not out:
            try:
                plans = self.http.get_json(EP_PLANS_FULL, bearer=token) or []
                for plan in plans:
                    out.extend(plan.get("included_groups") or [])
            except Exception:
                pass
        # De-duplicate by id, keeping first occurrence.
        seen, uniq = set(), []
        for g in out:
            gid = g.get("id")
            if gid is None or gid in seen:
                continue
            seen.add(gid)
            uniq.append(g)
        return uniq

    @staticmethod
    def pick_group(groups: list[dict], want: str) -> int | None:
        """Map a friendly key slot to a group id.

        Groups are named by role ("Essevin · claude", "Essevin · gpt_text"),
        not by the platform field. Match the name, then fall back to model ids.
        """
        want = want.lower()
        aliases = {
            "openai": ("gpt_text", "gpt", "openai"),
            "claude": ("claude",),
            "codex": ("gpt_text", "gpt", "openai"),
        }.get(want, (want,))
        for alias in aliases:
            for g in groups:
                name = (g.get("name") or "").lower()
                # Plan/subscription groups ("Essevin Plan · free_trial") are not
                # model groups; keys bound to them route nowhere useful.
                if "plan" in name:
                    continue
                plat = (g.get("platform") or "").lower()
                if alias in name or alias == plat:
                    return g.get("id")
        for alias in aliases:
            for g in groups:
                if any(alias in (m or "").lower() for m in (g.get("model_ids") or [])):
                    return g.get("id")
        return None

    def create_key(self, token: str, name: str, group_id: int) -> dict:
        self.log(f"create api key {name!r} in group {group_id}")
        # Shape mirrors the browser exactly; omitting group_id or expires_at
        # makes the backend answer 400 Invalid request.
        return self.http.post_json(
            EP_KEYS, {"name": name, "group_id": group_id, "expires_at": ""}, bearer=token)

    # -- orchestration -------------------------------------------------
    def run(self, *, email: str, share_url: str, key_name: str = "auto",
            mail_timeout: float = 180.0, code_pattern: str | None = None,
            password: str | None = None) -> RegisterResult:
        res = RegisterResult(email=email, identity=self.identity,
                             proxy=self.proxy.describe() if self.proxy else "direct")
        password = password or password_for()
        mail = ICloudMailClient(share_url)

        if self.proxy:
            try:
                res.egress_ip = check_egress(self.proxy)
                self.log(f"egress ip {res.egress_ip}")
            except Exception as exc:
                res.warn.append(f"egress check failed: {exc}")

        since = time.time()
        self.send_verify_code(email)
        code = mail.wait_code(since=since,
                              pattern=code_pattern or r"\b(\d{6})\b",
                              timeout=mail_timeout, log=self.log)
        self.verify_code(email, code)
        token, user = self.register(email, password, code)
        res.token = token
        res.user_id = user.get("id")

        try:
            me = self.me(token)
            res.balance = me.get("balance")
        except Exception as exc:
            res.warn.append(f"users/me failed: {exc}")

        # Keys are bound to a model group; resolve it instead of hardcoding an id.
        keys = []
        try:
            groups = self.groups(token)
            wanted = [key_name] if key_name != "auto" else ["openai", "claude"]
            for plat in wanted:
                gid = self.pick_group(groups, plat)
                if gid is None:
                    res.warn.append(f"no group for platform {plat!r}")
                    continue
                data = self.create_key(token, plat, gid)
                if data.get("key"):
                    keys.append({"platform": plat, "group_id": gid,
                                 "key": data["key"], "key_id": data.get("id")})
                else:
                    res.warn.append(f"api-keys returned no plaintext key for {plat}")
        except Exception as exc:
            res.warn.append(f"api key creation failed: {exc}")
        res.keys = keys
        if keys:
            res.api_key = keys[0]["key"]
            res.api_key_id = keys[0]["key_id"]

        return res
