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
    # 完整代理 URL：协议://用户:密码@IP:端口
    # 支持 socks5 / socks5h / http / https；zooproxy 也用同一格式
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

    @property
    def url(self) -> str:
        return self.raw_url

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
        # 代理列表：MISTRAL_PROXY_URLS（换行分隔的完整 URL）
        raw = __import__("os").environ.get("MISTRAL_PROXY_URLS", "").strip()
        urls = [u.strip() for u in raw.split("\n") if u.strip()]
        if urls:
            self._proxies = [Proxy.from_url(u) for u in urls]
        else:
            self._proxies = [Proxy.from_url(s) for s in (sids or [])]
        self._rr = itertools.cycle(range(len(self._proxies))) if self._proxies else None

    def all(self) -> list[Proxy]:
        return list(self._proxies)

    def by_index(self, n: int) -> Optional[Proxy]:
        """按序号轮转取代理；代理可重复使用（第 n 个用第 n%len 个）。"""
        with self._lock:
            if not self._proxies:
                return None
            return self._proxies[max(0, n) % len(self._proxies)]

    def rotated(self, start: int = 0) -> list[Proxy]:
        """从 start 开始按顺序轮转整个代理列表（确定性，便于复现）。"""
        with self._lock:
            n = len(self._proxies)
            if not n:
                return []
            start = max(0, int(start)) % n
            return [self._proxies[(start + i) % n] for i in range(n)]

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


# 代理列表统一从 MISTRAL_PROXY_URLS 读取（换行分隔的完整 URL）
DEFAULT_SIDS: list[str] = []
