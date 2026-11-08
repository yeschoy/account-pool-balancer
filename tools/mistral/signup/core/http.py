"""
HTTP 客户端：curl_cffi + 链式网关 + 浏览器指纹

curl_cffi 的价值：原生模拟 Chrome 的 TLS 指纹(JA3/JA4) + HTTP/2 帧序，
这是 Cloudflare 判断"是不是真浏览器"的核心依据之一。
"""
from __future__ import annotations

import time
from typing import Any, Optional

from curl_cffi import requests as creq

from core.chain_gateway import GatewayPool
from core.fingerprint import DeviceProfile


class MistralHTTP:
    """
    一个账号一个实例。
    代理: 每账号一个 zooproxy sid -> 本地 gateway 端口
    TLS : curl_cffi impersonate=chrome
    """

    def __init__(self, profile: DeviceProfile, proxy_url: str,
                 gateway_pool: GatewayPool, impersonate: str = "chrome",
                 timeout: int = 45):
        self.profile = profile
        self.local_proxy = gateway_pool.get(proxy_url)
        self.timeout = timeout
        self.s = creq.Session(impersonate=impersonate, timeout=timeout)
        self.s.proxies = {"http": self.local_proxy, "https": self.local_proxy}
        # 用我们自己的完整 header 集合覆盖默认值
        self.s.headers.clear()
        self.s.headers.update(profile.headers())

    # ---------- 通用 ----------
    def request(self, method: str, url: str, **kw) -> creq.Response:
        kw.setdefault("timeout", self.timeout)
        return self.s.request(method, url, **kw)

    def get(self, url: str, **kw):
        return self.request("GET", url, **kw)

    def post(self, url: str, **kw):
        return self.request("POST", url, **kw)

    def delete(self, url: str, **kw):
        return self.request("DELETE", url, **kw)

    # ---------- 站点专用头 ----------
    def with_origin(self, origin: str, referer_path: str = "/"):
        self.s.headers["Origin"] = origin
        self.s.headers["Referer"] = origin + referer_path

    def trpc_headers(self, source: str = "nextjs-react"):
        self.s.headers["x-trpc-source"] = source
        self.s.headers["Content-Type"] = "application/json"

    # ---------- 工具 ----------
    @staticmethod
    def human_delay(lo: float = 0.8, hi: float = 3.2):
        import random
        time.sleep(random.uniform(lo, hi))

    def cookies_dict(self) -> dict[str, str]:
        out = {}
        for c in getattr(self.s, "cookies", []) or []:
            try:
                out[c.name] = c.value
            except Exception:
                pass
        return out

    def cookie_header(self) -> str:
        d = self.cookies_dict()
        return "; ".join(f"{k}={v}" for k, v in d.items())
