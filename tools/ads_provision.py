#!/usr/bin/env python3
"""
ads_provision.py —— 一号一 profile 批量创建

设计原则（2026-11-15 实测驱动）
────────────────────────────────
一个账号 = 一个 AdsPower profile = 一套固定身份。全程不换。

创建 profile 时最容易犯的错，是**指纹参数和代理出口互相矛盾**：
实测踩过的坑 —— 加州 IP 配 America/New_York 时区、Windows UA 却有 macOS 字体。
所以本工具的顺序是**先探代理，再按代理的实测地理位置生成指纹**：

    1. 逐个探测代理出口（IP / 国家 / 城市 / 时区）
    2. 用实测时区写进 fingerprint_config
    3. UA 的 Chrome 版本与内核版本对齐
    4. 创建 profile，记录映射关系

用法
────
    # 从代理文件批量创建 10 个
    python3 tools/ads_provision.py --proxies proxies.txt -n 10

    # 只看会创建什么，不真的建
    python3 tools/ads_provision.py --proxies proxies.txt -n 10 --dry-run

    # 列出已有 profile 与映射
    python3 tools/ads_provision.py --list

    # 删除（谨慎：不可恢复，进回收站 30 天）
    python3 tools/ads_provision.py --delete k1xxxxx k1yyyyy

映射记录写在 tools/data/ads_profiles.json：
    {"profiles": [{"profile_id":..., "serial":..., "proxy":..., "ip":...,
                   "country":..., "city":..., "timezone":..., "created":...}]}
"""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
import time
import urllib.parse
import urllib.request
from dataclasses import dataclass, asdict
from datetime import datetime, timezone
from pathlib import Path

HERE = Path(__file__).resolve().parent
DATA = HERE / "data"
MAP_PATH = DATA / "ads_profiles.json"
API = "http://local.adspower.net:50325"

# --------------------------------------------------------------------------
# 常量
# --------------------------------------------------------------------------

# 本机实测安装的内核。profile 必须与它一致，否则启动报
# "SunBrowser xxx is not ready,please to download"。
KERNEL_VERSION = "153"

# 内核真实版本 → UA 里的 Chrome 版本。不一致会被"版本一致性"规则识别。
KERNEL_TO_UA_CHROME = {
    "153": "153.0.0.0",
    "147": "147.0.0.0",
    "146": "146.0.0.0",
}

# 时区 → 该时区常见的系统区域，用于生成自洽的 locale
TZ_TO_LANG = {
    "America/": ["en-US", "en"],
    "Europe/": ["en-GB", "en"],
    "Asia/": ["en-US", "en"],
}

# WebGL 伪装目标：与 Windows + Intel 平台自洽
WEBGL_VENDOR = "Google Inc. (Intel)"
WEBGL_RENDERER = (
    "ANGLE (Intel, Intel(R) UHD Graphics 630 (0x00003E9B) "
    "Direct3D11 vs_5_0 ps_5_0, D3D11)"
)


# --------------------------------------------------------------------------
# 本地 API
# --------------------------------------------------------------------------

class AdsApi:
    def __init__(self, api_key: str, base: str = API) -> None:
        self.key = api_key
        self.base = base.rstrip("/")

    def _req(self, path: str, payload: dict | None = None,
             timeout: float = 120.0) -> dict:
        url = f"{self.base}{path}"
        data = None
        headers = {"Authorization": f"Bearer {self.key}"}
        if payload is not None:
            data = json.dumps(payload).encode()
            headers["Content-Type"] = "application/json"
        req = urllib.request.Request(url, data=data, headers=headers)
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return json.loads(r.read().decode())

    def create(self, payload: dict) -> dict:
        return self._req("/api/v1/user/create", payload)

    def update(self, payload: dict) -> dict:
        return self._req("/api/v1/user/update", payload)

    def list(self, page: int = 1, size: int = 100) -> list[dict]:
        out: list[dict] = []
        while True:
            d = self._req(
                f"/api/v1/user/list?page={page}&page_size={size}", timeout=60)
            if d.get("code") != 0:
                raise RuntimeError(f"列取失败: {d.get('msg')}")
            batch = (d.get("data") or {}).get("list") or []
            out.extend(batch)
            if len(batch) < size:
                break
            page += 1
        return out

    def delete(self, profile_ids: list[str]) -> dict:
        # 删除是 POST + user_ids
        return self._req("/api/v1/user/delete",
                         {"user_ids": profile_ids})


# --------------------------------------------------------------------------
# 代理探测
# --------------------------------------------------------------------------

# 本机必须经系统代理(Clash)才能连上上游代理。这里复用 proxy_bridge。
LOCAL_BRIDGE = "127.0.0.1:17100"


@dataclass
class ProxyInfo:
    proxy: str
    alive: bool
    ip: str = ""
    country: str = ""
    city: str = ""
    tz: str = ""
    org: str = ""
    error: str = ""


class _Bridge:
    """
    为一个上游代理临时起一座本地桥（桥→Clash→上游）。

    为什么每个代理要单独起桥：桥的上游是**启动时固定**的。
    如果所有探测都走同一座桥，不同 session 的代理会探到同一个出口 IP，
    建出来的 profile 就全绑在同一个 IP 上 —— 一号一 IP 直接失效。
    """

    def __init__(self, upstream: str, port: int, sys_proxy: str = "127.0.0.1:10808"):
        self.upstream = upstream
        self.port = port
        self.sys_proxy = sys_proxy
        self.proc: subprocess.Popen | None = None

    def __enter__(self) -> "_Bridge":
        script = HERE / "proxy_bridge.py"
        self.proc = subprocess.Popen(
            [sys.executable, str(script),
             "--listen", f"127.0.0.1:{self.port}",
             "--via", self.sys_proxy,
             "--upstream", self.upstream],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        )
        # 等端口就绪
        for _ in range(20):
            time.sleep(0.4)
            try:
                import socket
                with socket.create_connection(("127.0.0.1", self.port), timeout=1):
                    return self
            except OSError:
                continue
        raise RuntimeError(f"桥启动超时 :{self.port}")

    def __exit__(self, *exc) -> None:
        if self.proc is not None:
            try:
                self.proc.terminate()
                self.proc.wait(timeout=5)
            except Exception:  # noqa: BLE001
                try:
                    self.proc.kill()
                except Exception:  # noqa: BLE001
                    pass


def probe_proxy(proxy: str, bridge_port: int = 17100,
                timeout: float = 45.0) -> ProxyInfo:
    """
    探测代理出口。为**这个代理单独起一座桥**，再经桥访问 ipinfo。

    失败时 alive=False，**不会**用默认值糊弄 —— 宁可跳过这个代理，
    也不要用错误的时区建出一个自相矛盾的 profile。
    """
    try:
        with _Bridge(proxy, bridge_port):
            r = subprocess.run(
                ["curl", "-sS", "-m", str(int(timeout)),
                 "-x", f"http://127.0.0.1:{bridge_port}",
                 "https://ipinfo.io/json"],
                capture_output=True, text=True, timeout=timeout + 10,
            )
        if r.returncode != 0:
            return ProxyInfo(proxy, False, error=r.stderr.strip()[:120])
        j = json.loads(r.stdout)
        return ProxyInfo(
            proxy=proxy, alive=True,
            ip=str(j.get("ip") or ""),
            country=str(j.get("country") or ""),
            city=str(j.get("city") or ""),
            tz=str(j.get("timezone") or ""),
            org=str(j.get("org") or ""),
        )
    except Exception as e:  # noqa: BLE001
        return ProxyInfo(proxy, False, error=f"{type(e).__name__}: {e}"[:120])


def parse_proxy(raw: str) -> dict | None:
    """把 socks5://user:pass@host:port 拆成 AdsPower 的字段。"""
    u = urllib.parse.urlparse(raw.strip())
    if not u.hostname:
        return None
    scheme = (u.scheme or "socks5").lower()
    return {
        "proxy_soft": "other",
        "proxy_type": "socks5" if scheme.startswith("socks5") else "http",
        "proxy_host": u.hostname,
        "proxy_port": str(u.port or 5000),
        "proxy_user": urllib.parse.unquote(u.username or ""),
        "proxy_password": urllib.parse.unquote(u.password or ""),
    }


# --------------------------------------------------------------------------
# 指纹生成
# --------------------------------------------------------------------------

def lang_for_tz(tz: str) -> list[str]:
    for prefix, langs in TZ_TO_LANG.items():
        if tz.startswith(prefix):
            return list(langs)
    return ["en-US", "en"]


def build_ua(kernel: str) -> str:
    chrome = KERNEL_TO_UA_CHROME.get(kernel, f"{kernel}.0.0.0")
    return (
        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
        f"(KHTML, like Gecko) Chrome/{chrome} Safari/537.36"
    )


def build_fingerprint(info: ProxyInfo, kernel: str = KERNEL_VERSION) -> dict:
    """
    按代理实测地理位置生成自洽的指纹。

    自洽性检查（每一条都是实测踩过的坑）：
      - 时区来自代理 IP，不是硬编码
      - 语言与地区匹配
      - UA 的 Chrome 版本 == 内核版本
      - platform 与 UA 一致（都是 Windows）
    """
    tz = info.tz or "America/New_York"
    return {
        "browser_kernel_config": {"version": kernel, "type": "chrome"},
        "ua": build_ua(kernel),
        "platform": "windows",
        # 显式指定时区：automatic_timezone=1 实测不生效，仍会保留旧值
        "automatic_timezone": "0",
        "timezone": tz,
        "language_switch": "1",
        "language": lang_for_tz(tz),
        "webgl": "2",
        "webgl_image": "1",
        "webgl_config": {
            "unmasked_vendor": WEBGL_VENDOR,
            "unmasked_renderer": WEBGL_RENDERER,
        },
        "canvas": "1",
        "audio": "1",
        "hardware_concurrency": "8",
        "device_memory": "8",
        "screen_resolution": "1920_1080",
    }


# --------------------------------------------------------------------------
# 映射表
# --------------------------------------------------------------------------

def load_map() -> dict:
    if MAP_PATH.exists():
        try:
            return json.loads(MAP_PATH.read_text(encoding="utf-8"))
        except Exception:  # noqa: BLE001
            pass
    return {"profiles": []}


def save_map(d: dict) -> None:
    DATA.mkdir(parents=True, exist_ok=True)
    MAP_PATH.write_text(json.dumps(d, ensure_ascii=False, indent=1),
                        encoding="utf-8")


def upsert_map(entry: dict) -> None:
    d = load_map()
    rows = [r for r in d["profiles"] if r.get("profile_id") != entry["profile_id"]]
    rows.append(entry)
    d["profiles"] = rows
    save_map(d)


# --------------------------------------------------------------------------
# 命令
# --------------------------------------------------------------------------

# --------------------------------------------------------------------------
# 常驻桥管理
#
# AdsPower profile 的代理字段只能填 host:port，不能填 socks5 凭据链，
# 而本机又必须经 Clash 才能连上 zooproxy。所以每个 profile 需要一座
# **常驻**的本地桥：profile → 127.0.0.1:PORT → Clash → zooproxy。
#
# 桥的端口按 profile 分配并记进映射表，重启机器后按记录重建。
# --------------------------------------------------------------------------

BRIDGE_PORT_BASE = 17200


def alloc_bridge_port(used: set[int]) -> int:
    p = BRIDGE_PORT_BASE
    while p in used:
        p += 1
    return p


def spawn_bridge(upstream: str, port: int,
                 sys_proxy: str = "127.0.0.1:10808") -> subprocess.Popen:
    """起一座常驻桥（不随父进程退出）。"""
    script = HERE / "proxy_bridge.py"
    return subprocess.Popen(
        [sys.executable, str(script),
         "--listen", f"127.0.0.1:{port}",
         "--via", sys_proxy,
         "--upstream", upstream],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        start_new_session=True,
    )


def cmd_create(api: AdsApi, proxies: list[str], count: int,
               prefix: str, dry_run: bool, bridge_port: int) -> int:
    if not proxies:
        print("没有可用代理。一号一 profile 必须绑定独立代理。", file=sys.stderr)
        return 1

    existing = {p["user_id"] for p in api.list()}
    print(f"现有 profile: {len(existing)} 个")

    made = 0
    idx = 0
    while made < count:
        if idx >= len(proxies):
            print(f"\n代理不够：需要 {count} 个，只有 {len(proxies)} 个。"
                  f"已创建 {made} 个。", file=sys.stderr)
            break
        raw = proxies[idx]
        idx += 1

        pc = parse_proxy(raw)
        if pc is None:
            print(f"  跳过无法解析的代理: {raw[:40]}")
            continue

        info = probe_proxy(raw, bridge_port=bridge_port)
        if not info.alive:
            print(f"  跳过不可用代理: {info.error}")
            continue
        if info.country != "US":
            print(f"  跳过非美国出口: {info.ip} {info.country}")
            continue

        name = f"{prefix}-{made + 1:03d}"
        fp = build_fingerprint(info)

        # 为这个 profile 分配一座常驻桥，profile 的代理指向它
        mp = load_map()
        used = {int(r.get("bridge_port") or 0) for r in mp["profiles"]}
        bport = alloc_bridge_port(used)
        proxy_cfg = {
            "proxy_soft": "other",
            "proxy_type": "http",
            "proxy_host": "127.0.0.1",
            "proxy_port": str(bport),
            "proxy_user": "",
            "proxy_password": "",
        }
        payload = {
            "name": name,
            "group_id": "0",
            "user_proxy_config": proxy_cfg,
            "fingerprint_config": fp,
        }

        print(f"\n[{made + 1}/{count}] {name}")
        print(f"    IP:   {info.ip} ({info.country} / {info.city})")
        print(f"    TZ:   {info.tz}")
        print(f"    UA:   ...Chrome/{KERNEL_TO_UA_CHROME.get(KERNEL_VERSION)}")
        print(f"    桥:   127.0.0.1:{bport} -> Clash -> {info.ip}")

        if dry_run:
            made += 1
            continue

        # 先起桥，再建 profile（否则 profile 启动时无网）
        try:
            spawn_bridge(raw, bport)
            time.sleep(1.2)
        except Exception as e:  # noqa: BLE001
            print(f"    桥启动失败: {e}", file=sys.stderr)
            continue

        try:
            r = api.create(payload)
        except Exception as e:  # noqa: BLE001
            print(f"    创建失败: {e}", file=sys.stderr)
            continue
        if r.get("code") != 0:
            print(f"    创建失败: {r.get('msg')}", file=sys.stderr)
            # 常见原因：套餐环境数已满
            if "paid" in str(r.get("msg", "")).lower():
                print("    （套餐限制，无法继续创建）", file=sys.stderr)
                break
            continue

        pid = (r.get("data") or {}).get("id") or ""
        serial = (r.get("data") or {}).get("serial_number") or ""
        print(f"    已创建: {pid} (no={serial})")

        upsert_map({
            "profile_id": pid,
            "serial": str(serial),
            "name": name,
            "proxy": raw,
            "bridge_port": bport,
            "ip": info.ip,
            "country": info.country,
            "city": info.city,
            "timezone": info.tz,
            "org": info.org,
            "created": datetime.now(timezone.utc).isoformat(),
        })
        made += 1
        # 创建间隔，避免触发上游频率限制
        time.sleep(1.5)

    print(f"\n完成: 创建 {made} 个，映射写入 {MAP_PATH}")
    return 0


def cmd_list(api: AdsApi) -> int:
    rows = api.list()
    mp = {r["profile_id"]: r for r in load_map()["profiles"]}
    print(f"共 {len(rows)} 个 profile\n")
    print(f"{'profile_id':14} {'no':>4} {'name':14} {'ip':16} {'country':8} "
          f"{'tz':22} proxy")
    print("-" * 110)
    for p in rows:
        pid = p.get("user_id") or ""
        m = mp.get(pid) or {}
        px = (p.get("user_proxy_config") or {}).get("proxy_host") or "-"
        print(f"{pid:14} {str(p.get('serial_number') or ''):>4} "
              f"{str(p.get('name') or '')[:14]:14} "
              f"{str(p.get('ip') or '-'):16} {str(p.get('ip_country') or ''):8} "
              f"{str(m.get('timezone') or '-'):22} {px}")
    print(f"\n有映射记录: {len(mp)} 个")
    return 0


def cmd_delete(api: AdsApi, ids: list[str]) -> int:
    if not ids:
        print("没有指定 profile_id", file=sys.stderr)
        return 1
    r = api.delete(ids)
    print(json.dumps(r, ensure_ascii=False))
    if r.get("code") == 0:
        d = load_map()
        d["profiles"] = [x for x in d["profiles"] if x.get("profile_id") not in ids]
        save_map(d)
    return 0


def cmd_bridges(rebuild: bool = False) -> int:
    """
    检查/重建各 profile 的常驻桥。

    桥是外部进程，机器重启后全部消失。profile 的代理字段指向
    127.0.0.1:PORT，端口没人监听 = profile 无法上网。
    所以重启后必须按映射表把桥重新拉起来。
    """
    import socket

    d = load_map()
    rows = d["profiles"]
    if not rows:
        print("映射表为空")
        return 0

    print(f"{'profile_id':14} {'port':>6} {'状态':8} {'ip':16} tz")
    print("-" * 74)
    for r in rows:
        pid = r.get("profile_id")
        port = int(r.get("bridge_port") or 0)
        if not port:
            print(f"{pid:14} {'-':>6} {'无桥':8}")
            continue
        alive = False
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=1):
                alive = True
        except OSError:
            alive = False

        if not alive and rebuild:
            try:
                spawn_bridge(r.get("proxy") or "", port)
                time.sleep(1.2)
                with socket.create_connection(("127.0.0.1", port), timeout=2):
                    alive = True
            except Exception as e:  # noqa: BLE001
                print(f"{pid:14} {port:>6} 重建失败 {e}")
                continue

        print(f"{pid:14} {port:>6} "
              f"{'OK' if alive else 'DOWN':8} "
              f"{str(r.get('ip') or ''):16} {r.get('timezone') or ''}")
    return 0


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description="AdsPower 一号一 profile 批量创建")
    ap.add_argument("--api-key", default="")
    ap.add_argument("--proxies", help="代理文件，每行一个")
    ap.add_argument("-n", "--count", type=int, default=1)
    ap.add_argument("--prefix", default="deploy")
    ap.add_argument("--dry-run", action="store_true")
    ap.add_argument("--list", action="store_true")
    ap.add_argument("--bridges", action="store_true",
                    help="检查各 profile 的常驻桥状态")
    ap.add_argument("--rebuild-bridges", action="store_true",
                    help="检查并重建已失效的桥（机器重启后用）")
    ap.add_argument("--delete", nargs="*", default=None)
    ap.add_argument("--bridge-port", type=int, default=17100,
                    help="本地代理桥端口（桥负责 本机→Clash→上游代理）")
    args = ap.parse_args(argv)

    import os
    key = args.api_key or os.environ.get("ADS_API_KEY", "")

    # 桥管理只读本地映射，不需要 API key
    if args.bridges or args.rebuild_bridges:
        return cmd_bridges(rebuild=args.rebuild_bridges)

    if not key:
        print("需要 --api-key 或 ADS_API_KEY", file=sys.stderr)
        return 1
    api = AdsApi(key)
    if args.list:
        return cmd_list(api)
    if args.delete is not None:
        return cmd_delete(api, args.delete)

    proxies: list[str] = []
    if args.proxies:
        p = Path(args.proxies)
        if not p.exists():
            print(f"代理文件不存在: {p}", file=sys.stderr)
            return 1
        for line in p.read_text(encoding="utf-8").splitlines():
            line = line.strip()
            if line and not line.startswith("#"):
                proxies.append(line)

    return cmd_create(api, proxies, args.count, args.prefix,
                      args.dry_run, args.bridge_port)


if __name__ == "__main__":
    raise SystemExit(main())
