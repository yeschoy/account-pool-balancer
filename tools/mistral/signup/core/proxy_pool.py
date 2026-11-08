"""
代理池：链式代理支持

本地必须有第一跳（系统代理），第二跳是 zooproxy 住宅池。

链路: 本机 -> 本地singbox(10808) -> zooproxy -> 目标站
curl 用 --preproxy 实现这两层。

zooproxy 端点注意: 协议是 HTTP，不是 SOCKS5（实测 407 Proxy Authentication）。
"""
from __future__ import annotations

import itertools
import random
import threading
import time
from dataclasses import dataclass, field
from typing import Optional


def _hostport_of(url: str) -> str:
    """socks5://u:p@1.2.3.4:1080 -> 1.2.3.4:1080"""
    import urllib.parse as up
    try:
        p = up.urlparse(url if "://" in url else "http://" + url)
        if p.hostname:
            return f"{p.hostname}:{p.port or ''}".rstrip(":")
    except Exception:
        pass
    return url[:40]


@dataclass
class Proxy:
    sid: str
    host: str = __import__("os").environ.get("ZOOPROXY_HOST", "us-eu.zooproxy.com")
    port: int = int(__import__("os").environ.get("ZOOPROXY_PORT", "5000"))
    user_tpl: str = __import__("os").environ.get(
        "ZOOPROXY_USER_TPL", "<user>-region-{region}-sid-{sid}-t-10")
    password: str = __import__("os").environ.get("ZOOPROXY_PASSWORD", "")
    region: str = "CA"
    # raw_url 非空时直接使用，忽略上面的模板字段。
    # 支持 socks5://user:pass@ip:port、http://、https://
    raw_url: str = ""

    # 运行时状态
    alive: bool = True
    fail_count: int = 0
    last_ok: float = 0.0
    exit_ip: Optional[str] = None
    cooldown_until: float = 0.0

    @classmethod
    def from_url(cls, url: str, label: str = "") -> "Proxy":
        """从完整代理 URL 构造。label 用于展示（默认取 host:port）。"""
        u = url.strip()
        label = label or _hostport_of(u)
        return cls(sid=label, raw_url=u)

    @classmethod
    def from_env(cls, sid: str) -> "Proxy":
        hp = __import__("os").environ.get("ZOOPROXY_HOST_PORT", "")
        if ":" in hp:
            h, p = hp.rsplit(":", 1)
            try:
                return cls(sid=sid, host=h, port=int(p))
            except ValueError:
                pass
        return cls(sid=sid)

    @property
    def url(self) -> str:
        if self.raw_url:
            return self.raw_url
        u = self.user_tpl.format(region=self.region, sid=self.sid)
        return f"http://{u}:{self.password}@{self.host}:{self.port}"

    @property
    def scheme(self) -> str:
        """socks5 / socks5h / http / https"""
        u = self.url
        return u.split("://", 1)[0].lower() if "://" in u else "http"

    @property
    def is_socks(self) -> bool:
        return self.scheme.startswith("socks")

    def __repr__(self) -> str:
        return f"<Proxy {self.sid} {self.exit_ip or '?'}>"


class ProxyPool:
    """
    线程安全的代理池。
    - acquire() 取一个可用代理（带粘性会话，一个账号一个 sid）
    - report_ok / report_fail 反馈健康度
    """

    def __init__(self, sids: list[str] | None = None,
                 pre_proxy: str = ""):
        # pre_proxy 为空表示直连（OVH 场景，无需本地第一跳）
        sids = sids if sids is not None else DEFAULT_SIDS
        self.pre_proxy = pre_proxy
        self._lock = threading.Lock()
        # 优先读完整 URL 列表（MISTRAL_PROXY_URLS，换行分隔）
        raw = __import__("os").environ.get("MISTRAL_PROXY_URLS", "").strip()
        if raw:
            urls = [u.strip() for u in raw.split("\n") if u.strip()]
            self._proxies = [Proxy.from_url(u) for u in urls]
        else:
            self._proxies = [Proxy.from_env(s) for s in sids]
        self._rr = itertools.cycle(range(len(self._proxies))) if self._proxies else None

    def all(self) -> list[Proxy]:
        return list(self._proxies)

    def by_index(self, n: int) -> Optional[Proxy]:
        """按序号轮转取代理；代理可重复使用（第 n 个用第 n%len 个）。"""
        with self._lock:
            if not self._proxies:
                return None
            return self._proxies[max(0, n) % len(self._proxies)]

    def acquire(self) -> Optional[Proxy]:
        with self._lock:
            now = time.time()
            candidates = [p for p in self._proxies if p.alive and p.cooldown_until <= now]
            if not candidates:
                # 全部冷却，挑冷却最早结束的
                candidates = sorted(self._proxies, key=lambda p: p.cooldown_until)[:1]
                if not candidates:
                    return None
            return random.choice(candidates)

    def report_ok(self, p: Proxy, exit_ip: str | None = None) -> None:
        with self._lock:
            p.fail_count = 0
            p.last_ok = time.time()
            if exit_ip:
                p.exit_ip = exit_ip

    def report_fail(self, p: Proxy, cooldown: float = 60.0) -> None:
        with self._lock:
            p.fail_count += 1
            if p.fail_count >= 5:
                p.alive = False
            p.cooldown_until = time.time() + cooldown * min(p.fail_count, 5)

    def healthy_count(self) -> int:
        now = time.time()
        return sum(1 for p in self._proxies if p.alive and p.cooldown_until <= now)


# 代理 sid 列表：从环境变量 ZOOPROXY_SIDS 读取（逗号分隔）
# 未设置时为空，需自行配置；示例见 cli.py 注释
import os as _os
DEFAULT_SIDS = [s for s in _os.environ.get("ZOOPROXY_SIDS", "").split(",") if s]
