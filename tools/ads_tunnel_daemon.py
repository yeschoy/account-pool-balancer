#!/usr/bin/env python3
"""
ads_tunnel_daemon.py —— Mac 侧的隧道守护（跨机架构的服务端）

为什么需要它
────────────
iOS/Mac 在 NAT 后面，OVH 无法主动 ssh 过来。而 CDP 端口是**动态**的
（每次启动 profile 都不同），没法预先写死一条 -R 隧道。

所以让 Mac 侧跑一个常驻守护，OVH 通过 HTTP 请求它按需建隧道：

    OVH                          Mac
     │  POST /tunnel?port=62841
     ├──────────────────────────► 守护进程
     │                             └─ ssh -N -R 16000:127.0.0.1:62841 ovh
     │  ◄── {"ok":true,"local_port":16000}
     │
     └─ 连 ws://127.0.0.1:16000/devtools/browser/...

同时守护还负责：
  - 转发 AdsPower API（固定 -R 15025）
  - 清理闲置隧道（profile 关闭后端口不再监听）

用法
────
    # Mac 上常驻
    python3 tools/ads_tunnel_daemon.py --listen 0.0.0.0:15026 \
        --ssh ovh-server

    # OVH 上
    export ADS_TUNNEL_DAEMON=http://<mac内网IP>:15026
    export ADS_API_BASE=http://127.0.0.1:15025
"""

from __future__ import annotations

import argparse
import json
import os
import socket
import subprocess
import sys
import threading
import time
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

# --------------------------------------------------------------------------
# 隧道管理
# --------------------------------------------------------------------------

API_TUNNEL_PORT = 15025        # OVH 侧看到 AdsPower API 的端口
CDP_LOCAL_BASE = 16000         # OVH 侧 CDP 隧道起始端口
MAC_API_PORT = 50325


class TunnelManager:
    def __init__(self, ssh: str) -> None:
        self.ssh = ssh
        self.lock = threading.Lock()
        self.tunnels: dict[int, dict] = {}   # mac_port -> {local, proc, at}
        self.api_proc: subprocess.Popen | None = None

    # -- 工具 --------------------------------------------------------

    @staticmethod
    def _port_open(port: int, host: str = "127.0.0.1",
                   timeout: float = 1.0) -> bool:
        try:
            with socket.create_connection((host, port), timeout=timeout):
                return True
        except OSError:
            return False

    def _alloc(self) -> int:
        used = {v["local"] for v in self.tunnels.values()}
        p = CDP_LOCAL_BASE
        while p in used:
            p += 1
        return p

    # -- API 隧道（固定） --------------------------------------------

    def ensure_api_tunnel(self) -> None:
        if self.api_proc is not None and self.api_proc.poll() is None:
            return
        cmd = [
            "ssh", "-N",
            "-o", "ExitOnForwardFailure=yes",
            "-o", "ServerAliveInterval=30",
            "-o", "ServerAliveCountMax=3",
            "-R", f"{API_TUNNEL_PORT}:127.0.0.1:{MAC_API_PORT}",
            self.ssh,
        ]
        self.api_proc = subprocess.Popen(
            cmd, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
            start_new_session=True)
        print(f"[daemon] API 隧道 -R {API_TUNNEL_PORT} -> :{MAC_API_PORT}",
              flush=True)

    # -- CDP 隧道（按需） --------------------------------------------

    def open_cdp(self, mac_port: int) -> dict:
        with self.lock:
            cur = self.tunnels.get(mac_port)
            if cur and cur["proc"].poll() is None:
                return {"ok": True, "local_port": cur["local"]}

            if not self._port_open(mac_port):
                return {"ok": False,
                        "error": f"Mac 上 :{mac_port} 未监听（profile 没起来？）"}

            local = self._alloc()
            cmd = [
                "ssh", "-N",
                "-o", "ExitOnForwardFailure=yes",
                "-o", "ServerAliveInterval=30",
                "-R", f"{local}:127.0.0.1:{mac_port}",
                self.ssh,
            ]
            proc = subprocess.Popen(
                cmd, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                start_new_session=True)
            self.tunnels[mac_port] = {"local": local, "proc": proc,
                                      "at": time.time()}
            print(f"[daemon] CDP 隧道 -R {local} -> :{mac_port}", flush=True)
            time.sleep(1.0)
            return {"ok": True, "local_port": local}

    def reap(self) -> None:
        """清掉 Mac 端已不再监听的 profile 隧道。"""
        with self.lock:
            dead = []
            for mp, info in list(self.tunnels.items()):
                if not self._port_open(mp):
                    try:
                        info["proc"].terminate()
                    except Exception:  # noqa: BLE001
                        pass
                    dead.append(mp)
                elif info["proc"].poll() is not None:
                    dead.append(mp)
            for mp in dead:
                self.tunnels.pop(mp, None)
                print(f"[daemon] 回收隧道 :{mp}", flush=True)


MGR: TunnelManager


# --------------------------------------------------------------------------
# HTTP
# --------------------------------------------------------------------------

class Handler(BaseHTTPRequestHandler):
    def log_message(self, *a) -> None:  # 安静
        pass

    def _json(self, code: int, obj: dict) -> None:
        body = json.dumps(obj, ensure_ascii=False).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self) -> None:
        u = urllib.parse.urlparse(self.path)
        if u.path == "/health":
            self._json(200, {"ok": True,
                             "tunnels": len(MGR.tunnels),
                             "api_tunnel": MGR.api_proc is not None
                             and MGR.api_proc.poll() is None})
            return
        if u.path == "/tunnels":
            self._json(200, {"ok": True, "tunnels": MGR.tunnels and {
                str(k): v["local"] for k, v in MGR.tunnels.items()}})
            return
        self._json(404, {"ok": False, "error": "not found"})

    def do_POST(self) -> None:
        u = urllib.parse.urlparse(self.path)
        if u.path != "/tunnel":
            self._json(404, {"ok": False, "error": "not found"})
            return
        q = urllib.parse.parse_qs(u.query)
        try:
            port = int((q.get("port") or ["0"])[0])
        except ValueError:
            self._json(400, {"ok": False, "error": "bad port"})
            return
        if port <= 0:
            self._json(400, {"ok": False, "error": "missing port"})
            return
        self._json(200, MGR.open_cdp(port))


def main(argv: list[str] | None = None) -> int:
    global MGR
    ap = argparse.ArgumentParser(description="Mac 侧隧道守护")
    ap.add_argument("--listen", default="0.0.0.0:15026")
    ap.add_argument("--ssh", default=os.environ.get("ADS_MAC_SSH", "ovh-server"))
    args = ap.parse_args(argv)

    host, _, port_s = args.listen.rpartition(":")
    MGR = TunnelManager(args.ssh)
    MGR.ensure_api_tunnel()

    srv = ThreadingHTTPServer((host or "0.0.0.0", int(port_s)), Handler)
    print(f"[daemon] 监听 {args.listen}，ssh 目标 {args.ssh}", flush=True)

    # 后台回收
    def loop() -> None:
        while True:
            time.sleep(20)
            try:
                MGR.reap()
                MGR.ensure_api_tunnel()
            except Exception as e:  # noqa: BLE001
                print(f"[daemon] reap err: {e}", flush=True)

    threading.Thread(target=loop, daemon=True).start()
    try:
        srv.serve_forever()
    except KeyboardInterrupt:
        pass
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
