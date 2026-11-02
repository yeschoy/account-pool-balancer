"""Logicc / Clerk HTTP clients. No browser, DOM, or JavaScript runtime required."""
from __future__ import annotations

import base64
from dataclasses import dataclass
from datetime import datetime, timezone
from email.utils import parsedate_to_datetime
from http.cookiejar import Cookie
import json
import math
import random
import re
import time
from typing import Any, Callable, Iterable
from urllib.parse import unquote, urlsplit, urlunsplit

from curl_cffi import CurlOpt, requests

ORIGIN = "https://app.logicc.com"
CLERK_BASE = "https://clerk.logicc.com/v1"
API_BASE = f"{ORIGIN}/api"
USER_AGENT = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36"
CLERK_VERSION = {"__clerk_api_version": "2025-11-10", "_clerk_js_version": "5.128.0"}


class ServiceError(RuntimeError):
    def __init__(self, service: str, status: int, codes: list[str], retry_after: float = 0):
        self.service, self.status, self.codes = service, status, codes
        self.retry_after = retry_after
        super().__init__(f"{service}: HTTP {status}, {', '.join(codes) or 'unknown_error'}")


class NetworkError(RuntimeError):
    pass


@dataclass(frozen=True, repr=False)
class ProxyConfig:
    url: str

    def __post_init__(self):
        try:
            value = urlsplit(self.url)
            valid = (value.scheme in {"http", "https", "socks5", "socks5h"}
                     and value.hostname and value.port and not value.query
                     and not value.fragment and value.path in {"", "/"})
        except ValueError:
            valid = False
        if not valid:
            raise ValueError("代理必须是 http(s)://host:port 或 socks5(h)://host:port，可包含认证")

    @property
    def label(self) -> str:
        value = urlsplit(self.url)
        return f"{value.scheme}://{value.hostname}:{value.port}"

    @property
    def request_url(self) -> str:
        value = urlsplit(self.url)
        return urlunsplit(("socks5h" if value.scheme == "socks5" else value.scheme,
                           value.netloc, "", "", ""))

    @property
    def authenticated_socks(self) -> bool:
        value = urlsplit(self.url)
        return value.scheme.startswith("socks") and value.username is not None

    def nopecha(self) -> dict:
        value = urlsplit(self.url)
        data = {"scheme": "socks5" if value.scheme == "socks5h" else value.scheme,
                "host": value.hostname, "port": value.port}
        if value.username is not None:
            data.update(username=unquote(value.username), password=unquote(value.password or ""))
        return data


class Pacer:
    def __init__(self, minimum: float = 1, maximum: float = 2,
                 log: Callable[[str], None] = print,
                 sleep: Callable[[float], None] = time.sleep):
        if not all(math.isfinite(x) for x in (minimum, maximum)) or not 0 <= minimum <= maximum <= 60:
            raise ValueError("步骤停顿需满足 0 <= 最小值 <= 最大值 <= 60 秒")
        self.minimum, self.maximum, self.log, self.sleep = minimum, maximum, log, sleep

    def pause(self, label: str, seconds: float | None = None) -> None:
        delay = random.uniform(self.minimum, self.maximum) if seconds is None else seconds
        if delay > 0:
            self.log(f"{label}，停顿 {delay:.1f} 秒")
            while delay > 0:
                chunk = min(delay, 15)
                self.sleep(chunk)
                delay -= chunk

