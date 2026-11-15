#!/usr/bin/env python3
"""
ads_remote.py —— 跨机驱动 AdsPower（Mac 跑浏览器，OVH 跑调度）

为什么需要这个
──────────────
AdsPower 是**本地客户端**架构：浏览器内核和 profile 数据（Cookie、
LocalStorage）都在装了客户端的机器上。而 OVH 那台是纯服务器，显卡是
ASPEED，Chromium 把它拉黑（WebGL1 blocklisted），跑指纹浏览器等于自废
武功。

所以正确分工是：

    Mac  (有真 GPU)  ──  AdsPower 客户端 + profile + 常驻桥 + 代理
      ▲
      │ SSH 反向隧道（API + CDP 都走这条）
      │
    OVH  (纯服务器)  ──  账号池调度、接码、数据库、号池入库

隧道建立后，OVH 上看到的是：
    http://127.0.0.1:15025/          → Mac 的 AdsPower API
    ws://127.0.0.1:<动态端口>/...     → 某个 profile 的 CDP

架构要点
────────
1. **API 隧道固定端口**（15025），常驻。
2. **CDP 隧道按 profile 动态分配**：profile 启动后才知道 debug_port，
   且它是 Mac 上的端口，OVH 侧要另开一条 -L 隧道转发过去。
3. **会话数据留在 Mac**。这是刻意的：profile 的 Cookie 在本机磁盘，
   搬到 OVH 反而会变成"新设备登录"触发风控。OVH 只存**账号元数据**
   （account_id / org_id / access_key / 绑卡信息），需要操作时经隧道驱动。

用法
────
    # 1. 在 Mac 上起 API 隧道（常驻）
    python3 tools/ads_remote.py tunnel-up

    # 2. 在 OVH 上（或任何能 ssh 到 Mac 的机器）驱动
    export ADS_REMOTE=1
    export ADS_API_BASE=http://127.0.0.1:15025
    python3 tools/ads_remote.py open k1j2oaa5

    # 3. 列 profile
    python3 tools/ads_remote.py list
"""

from __future__ import annotations

import argparse
import json
import os
import socket
import subprocess
import sys
import time
import urllib.parse
import urllib.request
from dataclasses import dataclass
from typing import Any

# --------------------------------------------------------------------------
# 隧道
# --------------------------------------------------------------------------

API_TUNNEL_PORT = 15025       # OVH 侧看到 AdsPower API 的端口
CDP_TUNNEL_BASE = 16000       # OVH 侧 CDP 隧道起始端口
MAC_API_PORT = 50325          # Mac 上 AdsPower 监听端口

# 默认 ssh 目标（在 OVH 上执行时，指回 Mac）。可在环境变量覆盖。
SSH_TARGET_ENV = "ADS_MAC_SSH"


def ssh_target() -> str:
    return os.environ.get(SSH_TARGET_ENV, "mac-ads")


def _port_open(host: str, port: int, timeout: float = 1.0) -> bool:
    try:
        with socket.create_connection((host, port), timeout=timeout):
            return True
    except OSError:
        return False


def tunnel_up(persist: bool = False) -> int:
    """
    起 API 隧道：Mac 的 50325 → OVH 的 15025。

    在**Mac 上**执行（主动向 OVH 建反向隧道）。若在 OVH 上执行，
    则用 -L 正向隧道（但需要 OVH 能 ssh 到 Mac，通常不成立）。

    常驻写法见 docs：建议用 autossh 或 systemd。
    """
    target = ssh_target()
    cmd = [
        "ssh", "-N",
        "-o", "ExitOnForwardFailure=yes",
        "-o", "ServerAliveInterval=30",
        "-o", "ServerAliveCountMax=3",
        "-R", f"{API_TUNNEL_PORT}:127.0.0.1:{MAC_API_PORT}",
        target,
    ]
    print("启动 API 隧道:")
    print("  " + " ".join(cmd))
    if persist:
        # 后台常驻
        p = subprocess.Popen(cmd, stdout=subprocess.DEVNULL,
                             stderr=subprocess.DEVNULL,
                             start_new_session=True)
        time.sleep(2)
        print(f"  后台 pid={p.pid}")
        return 0
    try:
        subprocess.run(cmd)
    except KeyboardInterrupt:
        pass
    return 0


class CdpTunnel:
    """
    为某个 profile 的 CDP 端口在 OVH 侧开一条隧道。

    ⚠️ 只能在 **Mac 侧**建 -R 隧道：OVH 无法主动 ssh 到 Mac（Mac 在 NAT 后）。
    所以这里提供一个 Mac 侧常驻守护（见 docs），暴露一个 HTTP 端点
    /tunnel?port=NNNN，由它执行：
        ssh -N -R <local>:127.0.0.1:<port> ovh-server
    并返回分配的 local 端口。

    如果 ADS_TUNNEL_DAEMON 未设置，说明在本机运行（不需要隧道），
    直接返回原端口。
    """

    def __init__(self, remote_port: int, local_port: int | None = None) -> None:
        self.remote_port = remote_port
        self.local_port = local_port or alloc_cdp_port(set())
        self.proc: subprocess.Popen | None = None

    def open(self) -> int:
        daemon = os.environ.get("ADS_TUNNEL_DAEMON", "").rstrip("/")
        if not daemon:
            # 本机运行：端口直接可用
            if _port_open("127.0.0.1", self.remote_port):
                self.local_port = self.remote_port
                return self.local_port
            raise RuntimeError(
                f"端口 :{self.remote_port} 不可达，且未配置 ADS_TUNNEL_DAEMON。"
                "跨机时必须先在 Mac 上启动隧道守护。")

        # 请求 Mac 侧的守护建隧道
        q = urllib.parse.urlencode({"port": self.remote_port})
        req = urllib.request.Request(f"{daemon}/tunnel?{q}", method="POST")
        with urllib.request.urlopen(req, timeout=60) as r:
            d = json.loads(r.read().decode())
        if not d.get("ok"):
            raise RuntimeError(f"守护建隧道失败: {d.get('error')}")
        self.local_port = int(d.get("local_port") or 0)
        for _ in range(25):
            time.sleep(0.4)
            if _port_open("127.0.0.1", self.local_port):
                return self.local_port
        raise RuntimeError(f"CDP 隧道端口 :{self.local_port} 未就绪")

    def close(self) -> None:
        if self.proc is not None:
            try:
                self.proc.terminate()
            except Exception:  # noqa: BLE001
                pass


def alloc_cdp_port(used: set[int]) -> int:
    p = CDP_TUNNEL_BASE
    while p in used:
        p += 1
    return p


# --------------------------------------------------------------------------
# 远程 API 客户端
# --------------------------------------------------------------------------

class RemoteAdsApi:
    """
    通过隧道访问 AdsPower API。

    与本地 AdsApi 的区别仅 base URL；但 profile 启动后返回的
    debug_port 是 **Mac 上的端口**，需要再开一条 CDP 隧道才能用。
    """

    def __init__(self, api_key: str,
                 base: str | None = None) -> None:
        self.key = api_key
        self.base = (base or os.environ.get(
            "ADS_API_BASE") or f"http://127.0.0.1:{API_TUNNEL_PORT}").rstrip("/")

    def _req(self, path: str, payload: dict | None = None,
             timeout: float = 180.0) -> dict:
        data = None
        headers = {"Authorization": f"Bearer {self.key}"}
        if payload is not None:
            data = json.dumps(payload).encode()
            headers["Content-Type"] = "application/json"
        req = urllib.request.Request(f"{self.base}{path}", data=data,
                                     headers=headers)
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return json.loads(r.read().decode())

    def list(self) -> list[dict]:
        d = self._req("/api/v1/user/list?page=1&page_size=200", timeout=60)
        if d.get("code") != 0:
            raise RuntimeError(f"列取失败: {d.get('msg')}")
        return (d.get("data") or {}).get("list") or []

    def start(self, profile_id: str) -> dict:
        d = self._req(
            f"/api/v1/browser/start?user_id={profile_id}"
            f"&cdp_mask=0&headless=0")
        if d.get("code") != 0:
            raise RuntimeError(f"启动失败: {d.get('msg')}")
        return d.get("data") or {}

    def stop(self, profile_id: str) -> dict:
        return self._req(f"/api/v1/browser/stop?user_id={profile_id}",
                         timeout=60)

    def local_active(self) -> list[dict]:
        d = self._req("/api/v1/browser/local-active", timeout=30)
        return (d.get("data") or {}).get("list") or []


def open_remote_profile(api: RemoteAdsApi, profile_id: str,
                        ssh: str | None = None) -> dict:
    """
    启动 profile 并建立 CDP 隧道，返回可在本机使用的 ws 地址。

    关键：AdsPower 返回的 ws 是 `ws://127.0.0.1:<mac_port>`，
    在 OVH 上直接连是连到**本机**的 127.0.0.1，必然失败。
    必须把那个端口通过 -L 映射过来，再把 ws URL 里的端口换掉。
    """
    data = api.start(profile_id)
    mac_port = int(data.get("debug_port") or 0)
    if not mac_port:
        raise RuntimeError(f"启动成功但没有 debug_port: {data}")

    # 已在本机？直接返回
    local_port = mac_port
    if not _port_open("127.0.0.1", mac_port):
        t = CdpTunnel(remote_port=mac_port)
        local_port = t.open()
        print(f"  CDP 隧道: 本机 :{local_port} -> Mac :{mac_port}")

    ws = ((data.get("ws") or {}).get("puppeteer") or "")
    ws = ws.replace(f"127.0.0.1:{mac_port}", f"127.0.0.1:{local_port}")
    return {"profile_id": profile_id, "ws": ws,
            "local_port": local_port, "mac_port": mac_port,
            "raw": data}


# --------------------------------------------------------------------------

def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description="跨机驱动 AdsPower")
    ap.add_argument("--api-key", default="")
    ap.add_argument("--base", default=None, help="API 地址（默认 127.0.0.1:15025）")
    ap.add_argument("--ssh", default=None, help="ssh 目标（开 CDP 隧道用）")
    sub = ap.add_subparsers(dest="cmd", required=True)

    sub.add_parser("tunnel-up", help="起 API 反向隧道（在 Mac 上执行）")
    sub.add_parser("list", help="列 profile")
    sp = sub.add_parser("open", help="启动 profile 并开 CDP 隧道")
    sp.add_argument("profile_id")
    sp.add_argument("--json", action="store_true")
    sd = sub.add_parser("stop", help="停止 profile")
    sd.add_argument("profile_id")

    args = ap.parse_args(argv)
    key = args.api_key or os.environ.get("ADS_API_KEY", "")

    if args.cmd == "tunnel-up":
        return tunnel_up()

    if not key:
        print("需要 --api-key 或 ADS_API_KEY", file=sys.stderr)
        return 1
    api = RemoteAdsApi(key, args.base)

    if args.cmd == "list":
        rows = api.list()
        print(f"共 {len(rows)} 个 profile（经隧道 {api.base}）\n")
        for p in rows:
            px = (p.get("user_proxy_config") or {}).get("proxy_port") or "-"
            print(f"  {p.get('user_id'):14} no={str(p.get('serial_number') or ''):>4} "
                  f"{str(p.get('name') or '')[:16]:16} ip={str(p.get('ip') or '-'):16} "
                  f"{str(p.get('ip_country') or ''):6} bridge={px}")
        return 0

    if args.cmd == "open":
        r = open_remote_profile(api, args.profile_id, args.ssh)
        if args.json:
            print(json.dumps({k: v for k, v in r.items() if k != "raw"},
                             ensure_ascii=False))
        else:
            print(f"profile: {r['profile_id']}")
            print(f"Mac 端口: {r['mac_port']}  本机映射: {r['local_port']}")
            print(f"ws: {r['ws']}")
        return 0

    if args.cmd == "stop":
        print(json.dumps(api.stop(args.profile_id), ensure_ascii=False))
        return 0

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
