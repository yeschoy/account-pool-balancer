#!/usr/bin/env python3
"""
本地代理桥：把 curl_cffi 的流量接到「本机系统代理 → 上游代理」这条链路上。

为什么需要它
────────────
本机只有在系统代理（如 Clash: 127.0.0.1:10808）开启时才能出外网。
而 curl_cffi 不支持多级代理：`--pre-proxy` 对 SOCKS5 上游不生效，
直连上游代理又因为没有出网通路而失败（得到 "Received invalid version"）。

本桥的做法：
    curl_cffi ──HTTP CONNECT──> 本桥(127.0.0.1:PORT)
                                    │
                                    └─CONNECT──> 系统代理 ──> 上游代理(SOCKS5) ──> 目标

于是 curl_cffi 只看到本地 HTTP 代理，TLS 指纹（JA3/JA4/H2）能力完全保留。

用法
────
    python3 tools/proxy_bridge.py --listen 127.0.0.1:17080 \
        --via 127.0.0.1:10808 \
        --upstream socks5://user:pass@host:port

然后在注册机里把代理填成：
    http://127.0.0.1:17080

也支持一条命令起多个（账号数 > 代理数时轮询复用同一个桥）。
"""

from __future__ import annotations

import argparse
import base64
import selectors
import socket
import socketserver
import struct
import sys
import threading
import time
import urllib.parse
from typing import Any

BUFSIZE = 65536


# --------------------------------------------------------------------------
# 与上游建立隧道（经系统代理）
# --------------------------------------------------------------------------

def connect_through_chain(
    sys_proxy: tuple[str, int] | None,
    upstream: str | None,
    target_host: str,
    target_port: int,
    timeout: float = 30.0,
) -> socket.socket:
    """
    建立到 target 的 socket。

    链路：本桥 -> (系统代理 HTTP CONNECT) -> (上游 SOCKS5) -> target
    upstream 为 None 时直连（不经任何代理）。
    """
    # 第一步：拿到一个通往「上游代理」或「目标」的 socket
    if upstream:
        u = urllib.parse.urlparse(upstream if "://" in upstream else "http://" + upstream)
        uh, up = u.hostname, u.port
        if not uh or not up:
            raise ValueError(f"上游代理地址无效: {upstream}")
        sock = _open_via_system_proxy(sys_proxy, uh, up, timeout)
        scheme = (u.scheme or "http").lower()
        if scheme.startswith("socks"):
            _socks5_handshake(sock, u, target_host, target_port, timeout)
        else:
            _http_connect(sock, u, target_host, target_port, timeout)
        return sock

    return _open_via_system_proxy(sys_proxy, target_host, target_port, timeout)


def _recv_exact(sock: socket.socket, n: int) -> bytes:
    buf = b""
    while len(buf) < n:
        chunk = sock.recv(n - len(buf))
        if not chunk:
            raise ConnectionError("upstream closed during handshake")
        buf += chunk
    return buf


def _open_via_system_proxy(
    sys_proxy: tuple[str, int] | None, host: str, port: int, timeout: float
) -> socket.socket:
    """经系统代理做 HTTP CONNECT；无系统代理则直连。"""
    if not sys_proxy:
        return socket.create_connection((host, port), timeout=timeout)

    ph, pp = sys_proxy
    s = socket.create_connection((ph, pp), timeout=timeout)
    req = f"CONNECT {host}:{port} HTTP/1.1\r\nHost: {host}:{port}\r\nProxy-Connection: keep-alive\r\n\r\n"
    s.sendall(req.encode())
    resp = b""
    while b"\r\n\r\n" not in resp:
        chunk = s.recv(4096)
        if not chunk:
            break
        resp += chunk
        if len(resp) > 65536:
            break
    if b"200" not in resp.split(b"\r\n", 1)[0]:
        s.close()
        raise ConnectionError(f"系统代理 CONNECT 失败: {resp[:120]!r}")
    return s


def _socks5_handshake(sock: socket.socket, u: Any, host: str, port: int, timeout: float) -> None:
    """在已建立的 socket 上完成 SOCKS5 握手 + 认证 + CONNECT。"""
    user = urllib.parse.unquote(u.username or "")
    pw = urllib.parse.unquote(u.password or "")
    if user or pw:
        sock.sendall(b"\x05\x02\x00\x02")
    else:
        sock.sendall(b"\x05\x01\x00")
    ver, method = _recv_exact(sock, 2)
    if ver != 0x05:
        raise ConnectionError(f"上游不是 SOCKS5（首字节 {ver:#x}）")
    if method == 0x02:
        if not user:
            raise ConnectionError("上游要求认证但未提供凭据")
        ub, pb = user.encode(), pw.encode()
        sock.sendall(b"\x01" + bytes([len(ub)]) + ub + bytes([len(pb)]) + pb)
        _, st = _recv_exact(sock, 2)
        if st != 0x00:
            raise ConnectionError(f"SOCKS5 认证失败 (status={st})")
    elif method != 0x00:
        raise ConnectionError(f"上游不接受我们的认证方式 ({method:#x})")

    hb = host.encode()
    sock.sendall(b"\x05\x01\x00\x03" + bytes([len(hb)]) + hb + struct.pack("!H", port))
    ver, rep, _rsv, atyp = _recv_exact(sock, 4)
    if rep != 0x00:
        raise ConnectionError(f"SOCKS5 CONNECT 被拒绝 (rep={rep})")
    if atyp == 0x01:
        _recv_exact(sock, 4 + 2)
    elif atyp == 0x03:
        n = _recv_exact(sock, 1)[0]
        _recv_exact(sock, n + 2)
    elif atyp == 0x04:
        _recv_exact(sock, 16 + 2)


def _http_connect(sock: socket.socket, u: Any, host: str, port: int, timeout: float) -> None:
    """上游是 HTTP 代理时，在其上再发一次 CONNECT。"""
    hdr = f"CONNECT {host}:{port} HTTP/1.1\r\nHost: {host}:{port}\r\n"
    if u.username:
        cred = base64.b64encode(
            f"{urllib.parse.unquote(u.username)}:{urllib.parse.unquote(u.password or '')}".encode()
        ).decode()
        hdr += f"Proxy-Authorization: Basic {cred}\r\n"
    hdr += "\r\n"
    sock.sendall(hdr.encode())
    resp = b""
    while b"\r\n\r\n" not in resp:
        chunk = sock.recv(4096)
        if not chunk:
            break
        resp += chunk
    if b"200" not in resp.split(b"\r\n", 1)[0]:
        raise ConnectionError(f"上游 HTTP CONNECT 失败: {resp[:120]!r}")


# --------------------------------------------------------------------------
# 转发
# --------------------------------------------------------------------------

def _pump(a: socket.socket, b: socket.socket) -> None:
    sel = selectors.DefaultSelector()
    for s in (a, b):
        s.setblocking(False)
        sel.register(s, selectors.EVENT_READ)
    try:
        while True:
            events = sel.select(timeout=120)
            if not events:
                break
            for key, _ in events:
                src = key.fileobj
                dst = b if src is a else a
                try:
                    data = src.recv(BUFSIZE)
                except (BlockingIOError, InterruptedError):
                    continue
                except OSError:
                    return
                if not data:
                    return
                try:
                    dst.setblocking(True)
                    dst.sendall(data)
                    dst.setblocking(False)
                except OSError:
                    return
    finally:
        sel.close()


class BridgeHandler(socketserver.BaseRequestHandler):
    # 由 server 注入
    sys_proxy: tuple[str, int] | None = None
    upstream: str | None = None
    timeout: float = 30.0

    def handle(self) -> None:
        try:
            self._handle()
        except Exception as e:  # noqa: BLE001
            try:
                self.request.sendall(b"HTTP/1.1 502 Bad Gateway\r\n\r\n")
            except OSError:
                pass
            print(f"[bridge] 连接失败: {type(e).__name__}: {e}", file=sys.stderr, flush=True)

    def _handle(self) -> None:
        sock = self.request
        sock.settimeout(self.timeout)
        buf = b""
        while b"\r\n\r\n" not in buf:
            chunk = sock.recv(4096)
            if not chunk:
                return
            buf += chunk
            if len(buf) > 65536:
                return
        head = buf.split(b"\r\n\r\n", 1)[0].decode("latin-1")
        lines = head.split("\r\n")
        method, target, _ver = (lines[0].split() + ["", "", ""])[:3]

        if method.upper() != "CONNECT":
            sock.sendall(b"HTTP/1.1 405 Only CONNECT\r\n\r\n")
            return

        host, _, port_s = target.rpartition(":")
        port = int(port_s or 443)

        upstream_sock = connect_through_chain(
            self.sys_proxy, self.upstream, host, port, timeout=self.timeout
        )
        try:
            sock.sendall(b"HTTP/1.1 200 Connection established\r\n\r\n")
            sock.settimeout(None)
            _pump(sock, upstream_sock)
        finally:
            try:
                upstream_sock.close()
            except OSError:
                pass


class BridgeServer(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True


def start_bridge(
    listen: tuple[str, int],
    sys_proxy: tuple[str, int] | None,
    upstream: str | None,
    timeout: float = 30.0,
) -> BridgeServer:
    handler = type(
        "Handler",
        (BridgeHandler,),
        {"sys_proxy": sys_proxy, "upstream": upstream, "timeout": timeout},
    )
    srv = BridgeServer(listen, handler)
    t = threading.Thread(target=srv.serve_forever, daemon=True)
    t.start()

    # 等 bind 真正生效再返回：否则调用方立刻发请求可能拿到 "Connection refused"
    deadline = time.time() + 2.0
    while time.time() < deadline:
        try:
            probe = socket.create_connection((listen[0], srv.server_address[1]), timeout=0.2)
            probe.close()
            break
        except OSError:
            time.sleep(0.02)
    return srv


def parse_hostport(s: str) -> tuple[str, int]:
    h, _, p = s.rpartition(":")
    return (h or "127.0.0.1", int(p))


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description="本地代理桥：系统代理 -> 上游代理")
    ap.add_argument("--listen", default="127.0.0.1:17080", help="本地监听地址")
    ap.add_argument("--via", default="127.0.0.1:10808", help="系统代理（留空字符串表示不用）")
    ap.add_argument("--upstream", required=True, help="上游代理，如 socks5://user:pass@host:port")
    args = ap.parse_args(argv)

    listen = parse_hostport(args.listen)
    sys_proxy = parse_hostport(args.via) if args.via else None
    srv = start_bridge(listen, sys_proxy, args.upstream)
    print(f"[bridge] 监听 http://{listen[0]}:{listen[1]}  "
          f"via {sys_proxy or '直连'}  upstream {args.upstream.split('@')[-1]}", flush=True)
    try:
        while True:
            time.sleep(3600)
    except KeyboardInterrupt:
        srv.shutdown()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
