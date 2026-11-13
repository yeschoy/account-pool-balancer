"""
本地链式代理网关

问题: curl/curl_cffi/requests 都只能配置一层代理。
需求: 本机 -> sing-box(10808) -> zooproxy(HTTP:5000) -> 目标

方案: 在本机起一个 SOCKS5 服务，它的 upstream 走「经 sing-box 连 zooproxy」的链。
      上层客户端只需把代理指向这个本地端口。

启动:
    gw = ChainGateway(pre_proxy="socks5h://127.0.0.1:10808")  # 示例：本机两级；OVH 用空串直连
    port = gw.start()          # 例如 15000
    # 之后用 socks5://127.0.0.1:{port} 作为代理即可

每个出口 IP 一个监听端口（按 sid 绑定）。
"""
from __future__ import annotations

import base64
import select
import socket
import struct
import threading
from typing import Optional


def _socks5_client_handshake(conn: socket.socket, auth: Optional[tuple[str, str]]) -> None:
    """作为 SOCKS5 客户端向 local sing-box 握手"""
    methods = b"\x00" if not auth else b"\x00\x02"
    conn.sendall(b"\x05" + bytes([len(methods)]) + methods)
    resp = conn.recv(2)
    if len(resp) < 2 or resp[0] != 5:
        raise OSError("upstream not socks5")
    if resp[1] == 0x02:
        if not auth:
            raise OSError("upstream requires auth")
        u, p = auth
        ub, pb = u.encode(), p.encode()
        conn.sendall(b"\x01" + bytes([len(ub)]) + ub + bytes([len(pb)]) + pb)
        if conn.recv(2)[1] != 0:
            raise OSError("upstream auth failed")
    elif resp[1] != 0x00:
        raise OSError(f"upstream no acceptable auth: {resp[1]}")


def _socks5_request(conn: socket.socket, host: str, port: int) -> None:
    hb = host.encode()
    conn.sendall(b"\x05\x01\x00\x03" + bytes([len(hb)]) + hb + struct.pack(">H", port))
    resp = conn.recv(10)
    if len(resp) < 2 or resp[1] != 0:
        raise OSError(f"socks5 connect failed code={resp[1] if len(resp) > 1 else '?'}")


def _http_connect(sock: socket.socket, host: str, port: int, auth: Optional[tuple[str, str]]) -> None:
    req = f"CONNECT {host}:{port} HTTP/1.1\r\nHost: {host}:{port}\r\nProxy-Connection: keep-alive\r\n"
    if auth:
        tok = base64.b64encode(f"{auth[0]}:{auth[1]}".encode()).decode()
        req += f"Proxy-Authorization: Basic {tok}\r\n"
    req += "\r\n"
    sock.sendall(req.encode())
    buf = b""
    while b"\r\n\r\n" not in buf:
        chunk = sock.recv(4096)
        if not chunk:
            break
        buf += chunk
    status = buf.split(b"\r\n", 1)[0].decode(errors="replace")
    if " 200" not in status:
        raise OSError(f"CONNECT rejected: {status} | {buf[:200]}")


def _connect_via(sock: socket.socket, host: str, port: int,
                 scheme: str, auth: Optional[tuple[str, str]]) -> None:
    """在已建立的 socket 上用上游协议连到 host:port"""
    if scheme == "socks5":
        _socks5_client_handshake(sock, auth)
        _socks5_request(sock, host, port)
    else:
        _http_connect(sock, host, port, auth)


class ChainGateway:
    """把「两层代理」包装成本地单层 SOCKS5 代理"""

    def __init__(self, pre_proxy: str, target_proxy: str,
                 pre_auth: Optional[tuple[str, str]] = None,
                 target_auth: Optional[tuple[str, str]] = None,
                 host: str = "127.0.0.1", port: int = 0):
        self.pre_host, self.pre_port, self.pre_auth, self.pre_scheme = self._parse(pre_proxy, pre_auth, 1080)
        self.tgt_host, self.tgt_port, self.tgt_auth, self.tgt_scheme = self._parse(target_proxy, target_auth, 5000)
        self.host, self.port = host, port
        self._sock: Optional[socket.socket] = None
        self._thread: Optional[threading.Thread] = None
        self._stop = threading.Event()

    @staticmethod
    def _parse(url: str, auth, default_port: int):
        """返回 (host, port, auth, scheme)。scheme 决定用 SOCKS5 还是 HTTP CONNECT。"""
        import urllib.parse as up
        if not url:
            return "", 0, None, ""
        p = up.urlparse(url if "://" in url else "http://" + url)
        a = auth
        if not a and p.username:
            a = (up.unquote(p.username), up.unquote(p.password or ""))
        scheme = (p.scheme or "http").lower()
        # socks5 / socks5h 都按 SOCKS5 处理（h 表示远端解析域名）
        if scheme.startswith("socks"):
            scheme = "socks5"
        default = default_port
        if scheme == "socks5" and default_port == 5000:
            default = 1080
        return p.hostname or "127.0.0.1", p.port or default, a, scheme

    # ---------- 生命周期 ----------
    def start(self) -> int:
        self._sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        self._sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        self._sock.bind((self.host, self.port))
        self._sock.listen(64)
        self.port = self._sock.getsockname()[1]
        self._thread = threading.Thread(target=self._serve, daemon=True)
        self._thread.start()
        return self.port

    def stop(self) -> None:
        self._stop.set()
        try:
            if self._sock:
                self._sock.close()
        except Exception:
            pass

    @property
    def socks_url(self) -> str:
        return f"socks5://{self.host}:{self.port}"

    def __enter__(self):
        self.start()
        return self

    def __exit__(self, *a):
        self.stop()

    # ---------- 服务循环 ----------
    def _serve(self):
        while not self._stop.is_set():
            try:
                client, _ = self._sock.accept()
            except OSError:
                break
            threading.Thread(target=self._handle, args=(client,), daemon=True).start()

    def _handle(self, client: socket.socket):
        upstream = None
        try:
            client.settimeout(90)
            # --- 与本地客户端做 SOCKS5 服务端握手 ---
            hdr = client.recv(2)
            if len(hdr) < 2 or hdr[0] != 5:
                return
            nmethods = hdr[1]
            client.recv(nmethods)
            client.sendall(b"\x05\x00")  # 不要求认证

            req = client.recv(4)
            if len(req) < 4:
                return
            atyp = req[3]
            if atyp == 1:
                host = socket.inet_ntoa(client.recv(4))
            elif atyp == 3:
                ln = client.recv(1)[0]
                host = client.recv(ln).decode()
            elif atyp == 4:
                host = socket.inet_ntop(socket.AF_INET6, client.recv(16))
            else:
                return
            port = struct.unpack(">H", client.recv(2))[0]

            # --- 建立链 ---
            if self.pre_host:
                # 两层：本机 -> pre_proxy -> target_proxy -> host:port
                upstream = socket.create_connection((self.pre_host, self.pre_port), timeout=40)
                if self.pre_scheme == "socks5":
                    _socks5_client_handshake(upstream, self.pre_auth)
                    _socks5_request(upstream, self.tgt_host, self.tgt_port)
                else:
                    _http_connect(upstream, self.tgt_host, self.tgt_port, self.pre_auth)
                _connect_via(upstream, host, port, self.tgt_scheme, self.tgt_auth)
            else:
                # 单层（OVH 直连场景）：直接连目标代理
                upstream = socket.create_connection((self.tgt_host, self.tgt_port), timeout=40)
                _connect_via(upstream, host, port, self.tgt_scheme, self.tgt_auth)

            client.sendall(b"\x05\x00\x00\x01" + b"\x00" * 6)
            self._pump(client, upstream)
        except Exception:
            try:
                client.sendall(b"\x05\x01\x00\x01" + b"\x00" * 6)
            except Exception:
                pass
        finally:
            for s in (client, upstream):
                try:
                    if s:
                        s.close()
                except Exception:
                    pass

    @staticmethod
    def _pump(a: socket.socket, b: socket.socket):
        socks = [a, b]
        while True:
            try:
                r, _, x = select.select(socks, [], socks, 120)
            except (OSError, ValueError):
                return
            if x or not r:
                return
            for s in r:
                other = b if s is a else a
                try:
                    data = s.recv(65536)
                except OSError:
                    return
                if not data:
                    return
                try:
                    other.sendall(data)
                except OSError:
                    return


class GatewayPool:
    """每个 sid 一个 gateway，复用"""

    def __init__(self, pre_proxy: str = ""):
        # 空字符串 = 直连第二跳（OVH 等海外机器无需本地第一跳）
        self.pre_proxy = pre_proxy
        self._gws: dict[str, ChainGateway] = {}
        self._lock = threading.Lock()

    def get(self, proxy_url: str) -> str:
        """传入 zooproxy 的 url，返回本地 socks 地址"""
        with self._lock:
            if proxy_url not in self._gws:
                gw = ChainGateway(pre_proxy=self.pre_proxy, target_proxy=proxy_url)
                gw.start()
                self._gws[proxy_url] = gw
            return self._gws[proxy_url].socks_url

    def close_all(self):
        with self._lock:
            for gw in self._gws.values():
                gw.stop()
            self._gws.clear()


# ---------------------------------------------------------------- 出口探测
#
# ⚠️ 2026-11-09 修正：健康检查绝不能再打目标站。
#
# 旧实现是 `curl --socks5-hostname ... https://v2.auth.mistral.ai/login`：
#   - UA 是 curl/8.x，JA3 是 curl 的，无 cookie
#   - 目标就是会被注册的那个域
#   - find_working_proxy 最多重试 6 次 => 同一 IP 最多 6 次 curl 探测
#   - 探测完 30~60 秒后，同一个 IP 上出现一个 "Chrome 156" 的注册
# 在 Mistral 侧日志里这就是 "先 curl 后注册"，注册机的教科书指纹。
#
# 现在改成打 Cloudflare 的 /cdn-cgi/trace：第三方回显，顺带把出口国解析出来，
# 一举解决"语言/时区/账单地址靠人工填 region"的地理矛盾问题。
TRACE_URL = "https://www.cloudflare.com/cdn-cgi/trace"


def probe_exit(local_socks: str, timeout: int = 25) -> dict:
    """通过本地 gateway 解析出口 IP / 国家 / 机房。不碰目标站。

    返回 {"ok","ip","country","colo","raw","error"}
    """
    import subprocess
    host_port = local_socks.replace("socks5://", "").replace("socks5h://", "")
    try:
        r = subprocess.run(
            ["curl", "-s", "-m", str(timeout), "--socks5-hostname", host_port, TRACE_URL],
            capture_output=True, text=True, timeout=timeout + 10)
        raw = (r.stdout or "").strip()
    except Exception as e:
        return {"ok": False, "ip": "", "country": "", "colo": "",
                "raw": "", "error": str(e)[:80]}

    kv: dict[str, str] = {}
    for line in raw.splitlines():
        if "=" in line:
            k, _, v = line.partition("=")
            kv[k.strip()] = v.strip()

    ip = kv.get("ip", "")
    country = kv.get("loc", "").upper()
    ok = bool(ip) and bool(country)
    return {
        "ok": ok,
        "ip": ip,
        "country": country,
        "colo": kv.get("colo", ""),
        "raw": raw[:400],
        "error": "" if ok else "trace parse failed",
    }


def find_working_proxy(pool, gw_pool: GatewayPool, target: str = "",
                       max_tries: int = 6, exclude_ips=None,
                       start_index: int = 0) -> tuple:
    """从代理池里找一个真能连上、且出口 IP 未被占用的代理。

    target 参数保留只为兼容旧调用，**不再被访问**。

    返回 (proxy, local_socks_url, exit_info)；失败返回 (None, None, {})。
    """
    exclude = {str(ip) for ip in (exclude_ips or []) if ip}
    tried = 0
    for p in pool.rotated(start_index):
        if tried >= max_tries:
            break
        if not p.alive:
            continue
        local = gw_pool.get(p.url)
        info = probe_exit(local)
        tried += 1
        if not info["ok"]:
            pool.report_fail(p)
            continue
        if info["ip"] in exclude:
            # 这个出口 IP 已经注册过号了，换下一个，不算失败
            continue
        pool.report_ok(p, exit_ip=info["ip"])
        return (p, local, info)
    return (None, None, {})
