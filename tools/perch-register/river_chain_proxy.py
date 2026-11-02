#!/usr/bin/env python3
"""链式代理：本机系统代理 → 上游 SOCKS5，一个账号固定一条上游 sid。

zooproxy 这类网关会校验「谁在连它」，本机公网 IP 不在白名单时直连必然 403；
先经本机系统代理（Clash/Surge 等）出网，再由上游网关落地，链路变成：
本机 → 系统代理 → 上游网关 → 目标站点。

Playwright / Chromium 不支持 SOCKS + 认证，也不支持代理链，因此这里把整条
链路在本地实现成一个**无认证 HTTP 代理**，浏览器只要 `--proxy-server` 指向它即可，
TLS 由浏览器自己端到端完成，证书校验不受影响。
"""
from __future__ import annotations

import base64
import secrets
import selectors
import socket
import threading
from dataclasses import dataclass
from pathlib import Path
from typing import Callable
from urllib.parse import unquote, urlsplit

from logicc_protocol import ProxyConfig

SYSTEM_PROXY_ENV = ("RIVER_SYSTEM_PROXY", "OPEN_DESIGN_SYSTEM_PROXY", "LOGICC_SYSTEM_PROXY")
MAX_HEADER = 64 * 1024


class ChainError(RuntimeError):
    """链式隧道无法建立，或上游拒绝了这次连接。"""


@dataclass(frozen=True)
class ChainRoute:
    """系统代理（可空=直连）→ 上游代理。"""

    system: str | None
    upstream: str

    def __post_init__(self) -> None:
        if self.system is not None:
            scheme = urlsplit(self.system).scheme
            if scheme not in {"http", "https"}:
                raise ValueError("系统代理必须是 http(s)://host:port（用于 CONNECT 隧道）")
            ProxyConfig(self.system)
        ProxyConfig(self.upstream)

    @property
    def label(self) -> str:
        hop = ProxyConfig(self.system).label if self.system else "直连"
        return f"{hop} → {ProxyConfig(self.upstream).label}"


def system_proxy_from_env() -> str | None:
    """读取环境变量里的系统代理；留空表示本机直接连上游网关。"""
    import os

    for name in SYSTEM_PROXY_ENV:
        value = (os.environ.get(name) or "").strip()
        if value:
            return value
    return None


def _basic(url: str) -> str | None:
    parts = urlsplit(url)
    if parts.username is None:
        return None
    raw = f"{unquote(parts.username)}:{unquote(parts.password or '')}"
    return base64.b64encode(raw.encode()).decode()


def _recv_until(sock: socket.socket, marker: bytes, limit: int = MAX_HEADER) -> bytes:
    data = b""
    while marker not in data:
        chunk = sock.recv(4096)
        if not chunk:
            raise ChainError("上游代理提前关闭连接")
        data += chunk
        if len(data) > limit:
            raise ChainError("上游代理响应头过长")
    return data


def _tls_wrap(sock: socket.socket, hostname: str) -> socket.socket:
    import ssl

    return ssl.create_default_context().wrap_socket(sock, server_hostname=hostname)


def _http_connect_handshake(sock: socket.socket, upstream: ProxyConfig,
                            target: tuple[str, int]) -> None:
    """在已经连到代理的 socket 上完成到 target 的 CONNECT（含 Basic 认证）。"""
    request = [f"CONNECT {target[0]}:{target[1]} HTTP/1.1",
               f"Host: {target[0]}:{target[1]}"]
    auth = _basic(upstream.url)
    if auth:
        request.append(f"Proxy-Authorization: Basic {auth}")
    sock.sendall(("\r\n".join(request) + "\r\n\r\n").encode())
    head = _recv_until(sock, b"\r\n\r\n")
    status = head.split(b"\r\n", 1)[0].decode("latin-1")
    code = int(status.split()[1]) if len(status.split()) > 1 else 0
    if code != 200:
        body = head.split(b"\r\n\r\n", 1)[1].decode("utf-8", "replace").strip()
        raise ChainError(f"代理拒绝 CONNECT：{status} {body}".strip())


def _http_connect(proxy: str, target: tuple[str, int], timeout: float) -> socket.socket:
    """经 http(s) 代理做 CONNECT，返回已建立隧道的 socket。"""
    parts = urlsplit(proxy)
    sock = socket.create_connection((parts.hostname, parts.port), timeout=timeout)
    try:
        if parts.scheme == "https":
            sock = _tls_wrap(sock, parts.hostname)
        _http_connect_handshake(sock, ProxyConfig(proxy), target)
        return sock
    except Exception:
        sock.close()
        raise


def _socks5_connect(sock: socket.socket, upstream: ProxyConfig,
                    target: tuple[str, int]) -> None:
    """在已有 socket 上完成 SOCKS5 握手（支持用户名密码认证与域名远端解析）。"""
    parts = urlsplit(upstream.url)
    methods = b"\x00\x02" if parts.username is not None else b"\x00"
    sock.sendall(b"\x05" + bytes([len(methods)]) + methods)
    reply = _recv_exact(sock, 2)
    if reply[0] != 5:
        raise ChainError("上游代理不是 SOCKS5")
    if reply[1] == 0x02:
        user = unquote(parts.username or "").encode()
        secret = unquote(parts.password or "").encode()
        sock.sendall(b"\x01" + bytes([len(user)]) + user + bytes([len(secret)]) + secret)
        if _recv_exact(sock, 2)[1] != 0:
            raise ChainError("上游代理认证失败")
    elif reply[1] != 0x00:
        raise ChainError("上游代理不接受当前认证方式")

    host = target[0]
    try:
        packed = socket.inet_aton(host)
        addr = b"\x01" + packed
    except OSError:
        try:
            packed6 = socket.inet_pton(socket.AF_INET6, host)
            addr = b"\x04" + packed6
        except OSError:
            name = host.encode("idna") if any(ord(c) > 127 for c in host) else host.encode()
            addr = b"\x03" + bytes([len(name)]) + name
    sock.sendall(b"\x05\x01\x00" + addr + target[1].to_bytes(2, "big"))
    head = _recv_exact(sock, 4)
    if head[0] != 5:
        raise ChainError("上游代理响应异常")
    if head[1] != 0:
        raise ChainError(f"上游代理拒绝连接（SOCKS5 状态码 {head[1]}）")
    atyp = head[3]
    extra = 4 if atyp == 1 else 16 if atyp == 4 else _recv_exact(sock, 1)[0]
    _recv_exact(sock, extra + 2)


def _recv_exact(sock: socket.socket, count: int) -> bytes:
    data = b""
    while len(data) < count:
        chunk = sock.recv(count - len(data))
        if not chunk:
            raise ChainError("上游代理提前关闭连接")
        data += chunk
    return data


def open_tunnel(route: ChainRoute, host: str, port: int, timeout: float = 30.0) -> socket.socket:
    """建立到 host:port 的完整隧道：系统代理 → 上游代理（SOCKS5 或 HTTP CONNECT）→ 目标。"""
    upstream = ProxyConfig(route.upstream)
    scheme = urlsplit(upstream.url).scheme
    if route.system:
        sock = _http_connect(route.system, (upstream_host(upstream), upstream_port(upstream)),
                             timeout)
        sock.settimeout(timeout)
    else:
        sock = socket.create_connection((upstream_host(upstream), upstream_port(upstream)),
                                        timeout=timeout)
    try:
        if scheme == "https":
            # 上游代理自身走 TLS（Open Design 代理池允许 https://）。
            sock = _tls_wrap(sock, upstream_host(upstream))
        if scheme in {"http", "https"}:
            _http_connect_handshake(sock, upstream, (host, port))
        else:
            _socks5_connect(sock, upstream, (host, port))
        sock.settimeout(None)
        return sock
    except Exception:
        sock.close()
        raise


def upstream_host(proxy: ProxyConfig) -> str:
    return urlsplit(proxy.url).hostname or ""


def upstream_port(proxy: ProxyConfig) -> int:
    return urlsplit(proxy.url).port or 0


class LocalChainProxy:
    """把一条 ChainRoute 暴露成本机无认证 HTTP 代理，供浏览器使用。"""

    def __init__(self, route: ChainRoute, *, on_error: Callable[[str], None] | None = None):
        self.route = route
        self.on_error = on_error
        self._server = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        self._server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        self._server.bind(("127.0.0.1", 0))
        self._server.listen(64)
        self._server.settimeout(0.5)
        self.port = self._server.getsockname()[1]
        self._stop = threading.Event()
        self._thread = threading.Thread(target=self._serve, name="chain-proxy", daemon=True)
        self._thread.start()

    @property
    def url(self) -> str:
        return f"http://127.0.0.1:{self.port}"

    def close(self) -> None:
        self._stop.set()
        self._thread.join(timeout=5)
        self._server.close()

    def __enter__(self) -> "LocalChainProxy":
        return self

    def __exit__(self, *_exc) -> None:
        self.close()

    def _serve(self) -> None:
        while not self._stop.is_set():
            try:
                client, _ = self._server.accept()
            except (socket.timeout, TimeoutError):
                continue
            except OSError:
                break
            threading.Thread(target=self._handle, args=(client,), daemon=True).start()

    def _fail(self, client: socket.socket, status: str, reason: str) -> None:
        if self.on_error:
            self.on_error(reason)
        try:
            client.sendall(f"HTTP/1.1 {status} {reason}\r\nContent-Length: 0\r\n\r\n".encode())
        except OSError:
            pass
        finally:
            client.close()

    def _handle(self, client: socket.socket) -> None:
        upstream_sock = None
        try:
            client.settimeout(30)
            head = _recv_until(client, b"\r\n\r\n")
            request_line = head.split(b"\r\n", 1)[0].decode("latin-1")
            parts = request_line.split()
            if len(parts) < 2:
                return self._fail(client, "400", "Bad Request")
            method, target = parts[0].upper(), parts[1]
            if method != "CONNECT":
                # 浏览器走 HTTPS，CONNECT 之外只需明确拒绝，避免当成开放代理转发。
                return self._fail(client, "405", "Tunnel Only")
            host, _, port = target.rpartition(":")
            if not host:
                return self._fail(client, "400", "Bad Request")
            upstream_sock = open_tunnel(self.route, host.strip("[]"), int(port))
            client.sendall(b"HTTP/1.1 200 Connection Established\r\n\r\n")
            client.settimeout(None)
            self._splice(client, upstream_sock)
        except ChainError as exc:
            self._fail(client, "502", exc.args[0][:80].replace(" ", "_") or "Bad_Gateway")
        except Exception as exc:  # noqa: BLE001 — 单条连接失败不影响后续账号
            self._fail(client, "502", type(exc).__name__)
        finally:
            for sock in (client, upstream_sock):
                if sock is not None:
                    try:
                        sock.close()
                    except OSError:
                        pass

    @staticmethod
    def _splice(a: socket.socket, b: socket.socket) -> None:
        selector = selectors.DefaultSelector()
        selector.register(a, selectors.EVENT_READ, b)
        selector.register(b, selectors.EVENT_READ, a)
        try:
            while True:
                events = selector.select(timeout=120)
                if not events:
                    return
                for key, _ in events:
                    data = key.fileobj.recv(65536)
                    if not data:
                        return
                    key.data.sendall(data)
        except OSError:
            return
        finally:
            selector.close()


def route_for(upstream: ProxyConfig, system: str | None = None) -> ChainRoute:
    return ChainRoute(system=system_proxy_from_env() if system is None else system,
                      upstream=upstream.request_url)


