#!/usr/bin/env python3
"""
deploy.com 注册机 —— 完整链路（接码 + 验证 + 可选绑卡）

被 account-pool-balancer 后端以子进程方式调用，也支持独立 CLI。

完整流程（2026-11-13 实测打通）：
  1. POST /api/register                     注册（201）
  2. 轮询接码地址取验证邮件                  GET /api/messages?alias_email&token&range=all
  3. 从邮件正文提取 verify token             https://deploy.com/verify-email?token=...
  4. GET /api/authenticate?token=...        验证邮箱，下发 hive-portal-production cookie
  5. GET /api/profile                       确认 is_verified=true
  6. （可选）POST /api/login                未直接可用时用密码登录
  7. （可选）绑卡 + 充值                     Stripe token -> payment_methods -> credit/buy

后端调用约定：
  python3 deploy_registrar.py --job-file /path/job.json --events - 
  每个事件一行 JSON 输出到 stdout，供后端解析。
"""

from __future__ import annotations

import argparse
import json
import os
import random
import re
import string
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from dataclasses import dataclass, field
from datetime import datetime
from pathlib import Path
from typing import Any, Callable, Iterable

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

# --------------------------------------------------------------------------
# deploy.com 契约（实测）
# --------------------------------------------------------------------------

BASE = "https://ajax-portal.deploy.com"
REGISTER_URL = f"{BASE}/api/register"
LOGIN_URL = f"{BASE}/api/login"
CSRF_URL = f"{BASE}/api/csrf"
PROFILE_URL = f"{BASE}/api/profile"
AUTHENTICATE_URL = f"{BASE}/api/authenticate"
ORIGIN = "https://deploy.com"
SIGNUP_REFERER = "https://deploy.com/signup?source=landing_login"
REDIRECT_URI = "https://deploy.com/explore"
# 登录入口（用户指定）：已有账号一律走这里，不重复注册
LOGIN_PAGE = "https://deploy.com/login?source=home-header"
LOGIN_REFERER = LOGIN_PAGE

DEFAULT_UA = (
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/157.0.0.0 Safari/537.36"
)

# 实测 7 个有效值；third_party_publication 前端有但服务端 400，已剔除
HOW_FOUND = [
    "search_engine", "google_ads", "linkedin_twitter",
    "billboard", "sales_outreach", "friend_colleague", "other",
]

REQUIRED_PRODUCTS = ["deepseek-v4.1-flash", "glm-5.3-flash"]
OPTIONAL_PRODUCTS = [
    "multimodal-language-model", "visual-moderation", "text-moderation",
    "audio-moderation", "ocr-moderation", "ai-generated-media",
    "ai-generated-music", "ocr", "contextual-scene-classification",
    "demographics", "celebrity-recognition", "celebrity-voice-detection",
    "likeness-detection", "logo-detection", "media-search", "speech-to-text",
    "translation", "custom-search", "arbiter", "detect",
]

DEFAULT_PASSWORD = "Str0ng!Passw0rd#2026"


# --------------------------------------------------------------------------
# 事件输出（供后端解析）
# --------------------------------------------------------------------------

def emit(kind: str, **kw: Any) -> None:
    payload = {"type": kind, "ts": time.strftime("%H:%M:%S"), **kw}
    print(json.dumps(payload, ensure_ascii=False), flush=True)


def log(msg: str) -> None:
    emit("log", message=msg)


# --------------------------------------------------------------------------
# HTTP 客户端
# --------------------------------------------------------------------------

@dataclass
class Response:
    status: int
    body: str
    headers: dict[str, str] = field(default_factory=dict)

    @property
    def json(self) -> Any:
        try:
            return json.loads(self.body)
        except Exception:
            return None

    @property
    def retry_after(self) -> float | None:
        m = re.search(r"retry in (\d+)s", self.body)
        if m:
            return float(m.group(1))
        return None


class HttpClient:
    """带 cookie jar 的客户端，支持 curl_cffi 指纹与代理。"""

    def __init__(
        self,
        timeout: float = 30.0,
        ua: str = DEFAULT_UA,
        transport: str = "curl_cffi",
        profile: Any = None,
        proxy: str | None = None,
    ) -> None:
        self.timeout = timeout
        self.ua = ua
        self.cookies: dict[str, str] = {}
        self.transport = transport
        self.profile = profile
        self.proxy = proxy
        self._session = None
        if transport == "curl_cffi":
            from curl_cffi.requests import Session  # type: ignore

            self._session = Session()

    def _absorb(self, raw: str) -> None:
        first = raw.split(";", 1)[0]
        if "=" not in first:
            return
        k, v = first.split("=", 1)
        k, v = k.strip(), v.strip()
        if "Max-Age=0" in raw:
            self.cookies.pop(k, None)
        else:
            self.cookies[k] = v

    def request(
        self,
        method: str,
        url: str,
        payload: dict | None = None,
        extra_headers: dict[str, str] | None = None,
    ) -> Response:
        headers = {
            "Accept": "application/json, text/plain, */*",
            "Accept-Language": "en-US,en;q=0.9",
            "Origin": ORIGIN,
            "Referer": SIGNUP_REFERER,
            "User-Agent": self.ua,
            "X-Forwarded-Proto": "https",
        }
        if self.profile is not None:
            headers.update(self.profile.headers())
        if self.cookies:
            headers["Cookie"] = "; ".join(f"{k}={v}" for k, v in self.cookies.items())
        data = None
        if payload is not None:
            data = json.dumps(payload, ensure_ascii=False).encode()
            headers["Content-Type"] = "application/json"
        if extra_headers:
            headers.update(extra_headers)

        if self.transport == "curl_cffi" and self._session is not None:
            kw: dict = {"headers": headers, "timeout": self.timeout, "allow_redirects": False}
            if data is not None:
                kw["data"] = data
            if self.proxy:
                kw["proxies"] = {"http": self.proxy, "https": self.proxy}
            if self.profile is not None:
                fk = self.profile.request_kwargs()
                fk.pop("headers", None)
                kw.update(fk)
            # 代理链路（本机代理 → 上游）偶发失败：网络层错误重试 2 次
            last: Exception | None = None
            for attempt in range(3):
                try:
                    r = self._session.request(method, url, **kw)  # type: ignore[union-attr]
                    break
                except Exception as e:  # noqa: BLE001
                    last = e
                    if attempt < 2:
                        time.sleep(0.8 * (attempt + 1))
            else:
                return Response(0, f"network error: {type(last).__name__}: {last}", {})
            try:
                for sc in r.headers.get_list("Set-Cookie"):
                    self._absorb(sc)
            except Exception:  # noqa: BLE001
                pass
            return Response(r.status_code, r.text, {k.lower(): v for k, v in r.headers.items()})

        req = urllib.request.Request(url, data=data, headers=headers, method=method)
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as r:
                for sc in r.headers.get_all("Set-Cookie") or []:
                    self._absorb(sc)
                return Response(r.status, r.read().decode("utf-8", "replace"), dict(r.headers))
        except urllib.error.HTTPError as e:
            for sc in e.headers.get_all("Set-Cookie") or []:
                self._absorb(sc)
            return Response(e.code, e.read().decode("utf-8", "replace"), dict(e.headers))
        except urllib.error.URLError as e:
            return Response(0, f"network error: {e.reason}", {})


# --------------------------------------------------------------------------
# 接码平台（iCloud HME）
# --------------------------------------------------------------------------

MAILBOX_RE = re.compile(r"^https://([^/]+)/messages/([^?]+)\?token=([^&]+)$")


@dataclass
class Mailbox:
    email: str
    url: str
    host: str
    token: str
    alias: str

    @classmethod
    def parse(cls, email: str, url: str) -> "Mailbox":
        m = MAILBOX_RE.match(url.strip())
        if not m:
            # 兼容不带 /messages/ 的写法：只有 host 和 token
            u = urllib.parse.urlparse(url)
            q = urllib.parse.parse_qs(u.query)
            if "token" not in q:
                raise ValueError(f"无法解析接码地址: {url}")
            return cls(email=email, url=url, host=u.hostname or "", token=q["token"][0],
                       alias=urllib.parse.quote(email, safe=""))
        return cls(email=email, url=url, host=m.group(1), token=m.group(3),
                   alias=urllib.parse.unquote(m.group(2)))


class MailboxReader:
    """从 iCloud HME 接码地址读取验证邮件。"""

    def __init__(self, mb: Mailbox, timeout: float = 25.0) -> None:
        self.mb = mb
        self.timeout = timeout

    def fetch(self, range_: str = "all") -> list[dict]:
        q = urllib.parse.urlencode({
            "alias_email": self.mb.email,
            "token": self.mb.token,
            "range": range_,
        })
        url = f"https://{self.mb.host}/api/messages?{q}"
        req = urllib.request.Request(url, headers={"User-Agent": DEFAULT_UA, "Accept": "application/json"})
        with urllib.request.urlopen(req, timeout=self.timeout) as r:
            data = json.loads(r.read().decode("utf-8", "replace"))
        if not data.get("ok"):
            raise RuntimeError(f"接码接口返回失败: {data}")
        return data["data"]["emails"]

    def ids(self) -> set:
        return {e.get("id") for e in self.fetch("all")}

    @staticmethod
    def _received_ts(e: dict) -> float | None:
        """把邮件的 receivedAt（mida 的 ISO-8601 UTC）转成 epoch 秒。

        取信必须以**到达时间**为准，不能用邮件 id 增量：mida 的 id 在同一
        别名里单调递增，但一次重发可能让多条注册共用同一个基准集合；而
        上一轮轮询已经取到的邮件会被 after_ids 跳过，导致"信就在箱子里却
        被判为旧信"。receivedAt 是唯一可靠的时序依据。
        """
        raw = e.get("receivedAt") or e.get("createdAt")
        if not raw:
            return None
        try:
            s = str(raw).replace("Z", "+00:00")
            # 截断到微秒：Go/Python 都能解析，防止多余精度报错
            s = re.sub(r"(\.\d{6})\d+", r"\1", s)
            return datetime.fromisoformat(s).timestamp()
        except Exception:  # noqa: BLE001
            return None

    def wait_for_link(
        self,
        after_ids: set,
        timeout: float = 240.0,
        poll: float = 5.0,
        since_ts: float | None = None,
    ) -> tuple[str, dict] | None:
        """等待 deploy 发来的验证链接，返回 (token, email_obj)。

        since_ts: 只认到达时间不早于它的邮件（重发验证邮件时用注册时刻）。
        兼容旧行为：不传 since_ts 时仍按 after_ids 增量判断，但额外接受
        "receivedAt 晚于本次调用开始时刻"的邮件，避免漏掉入库但 id 未变的信。
        """
        started = time.time()
        baseline = started if since_ts is None else since_ts
        deadline = time.time() + timeout
        scanned = 0
        while time.time() < deadline:
            try:
                mails = self.fetch("all")
            except Exception as e:  # noqa: BLE001
                emit("log", message=f"取信失败，重试: {e}")
                time.sleep(poll)
                continue
            scanned = len(mails)
            # 收集本轮所有合格的验证信，取**最新**那封：
            # 重新认证接码会扫全箱，旧的验证 token 可能已失效或对应旧注册，
            # 最新的才是这次该用的。
            best: tuple[float, str, dict] | None = None
            for e in mails:
                ts = self._received_ts(e)
                # 有到达时间就以时间为准；没有才退回 id 增量
                if ts is not None:
                    if ts < baseline:
                        continue
                elif e.get("id") in after_ids:
                    continue
                blob = (e.get("textContent") or "") + " " + (e.get("htmlContent") or "")
                if "deploy.com" not in blob and "deploy" not in (e.get("fromAddress") or "").lower():
                    continue
                m = re.search(r"https://deploy\.com/verify-email\?token=([A-Za-z0-9_\-]+)", blob)
                if m:
                    rank = ts if ts is not None else 0.0
                    if best is None or rank >= best[0]:
                        best = (rank, m.group(1), e)
            if best is not None:
                return best[1], best[2]
            remain = int(deadline - time.time())
            emit("log", message=f"等待验证邮件… 剩余 {remain}s（收件箱 {scanned} 封）")
            time.sleep(poll)
        # 超时也说明白：是完全没有信，还是有信但时间对不上
        emit("log", message=f"等待 {timeout:.0f}s 超时，收件箱共 {scanned} 封，"
                            f"没有到达时间晚于 {baseline:.0f} 的 deploy 验证信")
        return None



# --------------------------------------------------------------------------
# 代理
# --------------------------------------------------------------------------

class ProxyBridge:
    """
    本地代理桥管理器。

    背景（2026-11-13 实测）：
      本机只有经系统代理（如 Clash 127.0.0.1:10808）才能出外网；
      zooproxy 的 `socks5://` 端点必须**经系统代理隧道**才能握手成功，
      直连会得到 `HTTP/1.1 403`（此前误判为「IP 未白名单」，实为无出网通路）。

    curl_cffi 不支持多级代理（PRE_PROXY 对 SOCKS5 上游无效），
    所以为每个上游代理起一个本地 HTTP CONNECT 桥：

        curl_cffi --CONNECT--> 127.0.0.1:PORT --> 系统代理 --> zooproxy --> 目标

    这样 curl_cffi 只看到本地 HTTP 代理，TLS/JA3 指纹能力完整保留。

    账号数 > 代理数时，按 i % len(proxies) 轮询复用同一个桥。
    """

    def __init__(self, sys_proxy: str = "127.0.0.1:10808", base_port: int = 17500) -> None:
        self.sys_proxy = self._parse(sys_proxy) if sys_proxy else None
        self.base_port = base_port
        self._servers: dict[str, tuple[Any, int]] = {}
        self._lock = threading.Lock()

    @staticmethod
    def _parse(s: str) -> tuple[str, int]:
        h, _, p = s.rpartition(":")
        return (h or "127.0.0.1", int(p))

    def endpoint_for(self, upstream: str) -> str:
        """
        返回该上游代理对应的本地桥地址（首次调用时启动桥）。

        端口冲突自动顺延：上一个进程异常退出时桥可能仍占着端口，
        不能让整个注册任务因此失败。
        """
        key = upstream.strip()
        with self._lock:
            hit = self._servers.get(key)
            if hit:
                return f"http://127.0.0.1:{hit[1]}"
            import proxy_bridge  # noqa: PLC0415

            last_err: Exception | None = None
            for offset in range(50):
                port = self.base_port + len(self._servers) + offset
                try:
                    srv = proxy_bridge.start_bridge(("127.0.0.1", port), self.sys_proxy, key)
                except OSError as e:  # 端口占用等
                    last_err = e
                    continue
                self._servers[key] = (srv, port)
                if offset:
                    emit("log", message=f"代理桥端口顺延 {offset} 位 -> {port}")
                emit("log", message=f"代理桥 @127.0.0.1:{port} -> {key.split('@')[-1]}")
                return f"http://127.0.0.1:{port}"
            raise RuntimeError(f"无法启动代理桥（{last_err}）")

    def close(self) -> None:
        with self._lock:
            for srv, _ in self._servers.values():
                try:
                    srv.shutdown()
                except Exception:  # noqa: BLE001
                    pass
            self._servers.clear()


def normalize_proxy(raw: str) -> str:
    """只做最基础的补齐，协议由桥负责。"""
    raw = raw.strip()
    if not raw:
        return ""
    if "://" not in raw:
        raw = "http://" + raw
    return raw


def pick_proxy(proxies: list[str], index: int) -> str:
    """轮询分配：账号数与代理数不必相等，按 index 取模复用。"""
    if not proxies:
        return ""
    return normalize_proxy(proxies[index % len(proxies)])


# --------------------------------------------------------------------------
# 指纹
# --------------------------------------------------------------------------

def load_fp_layer():
    try:
        import fp_layer  # noqa: PLC0415

        return fp_layer
    except Exception:  # noqa: BLE001
        return None


def pick_profile(seed: int | None = None):
    fp = load_fp_layer()
    if fp is None:
        return None
    try:
        profiles = fp.assign_profiles(1, seed=seed)
        return profiles[0] if profiles else None
    except Exception:  # noqa: BLE001
        return None


# --------------------------------------------------------------------------
# payload
# --------------------------------------------------------------------------

_FIRST = ["Ava", "Noah", "Mia", "Liam", "Zoe", "Ethan", "Ivy", "Owen", "Ruby", "Leo",
          "Nina", "Kai", "Elsa", "Theo", "Luna", "Ryan", "Maya", "Adam", "Iris", "Sean"]
_LAST = ["Chen", "Park", "Reed", "Brooks", "Hayes", "Ward", "Cole", "Lin", "Diaz", "Kim",
         "Shaw", "Ford", "Grant", "Lee", "Hale", "Ross", "Quinn", "Blake", "Chase", "Wang"]
_FIRMS = ["Northwind Labs", "Helix Robotics", "Cobalt Data", "Vertex Systems", "Lumen Studio",
          "Orbit Analytics", "Pinegrove AI", "Cascade Media", "Bluefin Health", "Tessera Labs"]


def pick_products() -> list[str]:
    extras = random.sample(OPTIONAL_PRODUCTS, k=random.randint(0, 4))
    items = list(REQUIRED_PRODUCTS) + extras
    random.shuffle(items)
    return items


def build_payload(email: str, password: str, name: str | None = None,
                  company: str | None = None, phone: str = "") -> dict:
    return {
        "name": name or f"{random.choice(_FIRST)} {random.choice(_LAST)}",
        "email": email,
        "password": password,
        "company": company or random.choice(_FIRMS),
        "phone": phone,
        "terms_of_service": True,
        "product_interests": pick_products(),
        "how_did_you_find_us": [random.choice(HOW_FOUND)],
        "source": "signup_from_landing_login",
        "redirect_uri": REDIRECT_URI,
    }


def gen_password() -> str:
    return DEFAULT_PASSWORD


# --------------------------------------------------------------------------
# 注册机
# --------------------------------------------------------------------------

class Registrar:
    def __init__(self, http: HttpClient, min_interval: float = 8.0, max_retries: int = 6) -> None:
        self.http = http
        self.min_interval = min_interval
        self.max_retries = max_retries
        self._lock = threading.Lock()
        self._last = 0.0

    def _throttle(self) -> None:
        with self._lock:
            wait = self._last + self.min_interval - time.monotonic()
            if wait > 0:
                time.sleep(wait)
            self._last = time.monotonic()

    def register(self, payload: dict) -> Response:
        backoff = 1.0
        resp = Response(0, "")
        for attempt in range(self.max_retries + 1):
            self._throttle()
            resp = self.http.request("POST", REGISTER_URL, payload)
            if resp.status == 429 and attempt < self.max_retries:
                base = resp.retry_after or 8.0
                delay = base * 1.2 * backoff + random.uniform(1.0, 3.0)
                backoff *= 1.6
                emit("log", message=f"429 限流，{delay:.1f}s 后重试 ({attempt + 1}/{self.max_retries})")
                time.sleep(delay)
                continue
            return resp
        return resp

    def authenticate(self, token: str) -> Response:
        q = urllib.parse.urlencode({"token": token})
        return self.http.request("GET", f"{AUTHENTICATE_URL}?{q}")

    def profile(self) -> Response:
        return self.http.request("GET", PROFILE_URL)

    def login(self, email: str, password: str) -> Response:
        """登录：每次取新鲜 csrf，body 带 token，cookie 里也要有。"""
        c = self.http.request("GET", CSRF_URL)
        csrf = ((c.json or {}).get("data") or {}).get("csrf")
        payload = {"email": email, "password": password}
        if csrf:
            payload["csrf"] = csrf
        self._throttle()
        return self.http.request("POST", f"{LOGIN_URL}?enforce_csrf=true", payload,
                                 extra_headers={"Referer": LOGIN_REFERER})

    def login_page(self) -> Response:
        """GET https://deploy.com/login?source=home-header，让上游侧看到真实登录入口。"""
        return self.http.request("GET", LOGIN_PAGE)


# 上游对重复注册的响应文案（201 但正文/邮件说"账号已存在"）
ALREADY_EXISTS_RE = re.compile(
    r"(already\s+(?:exists|registered|in\s+use))"
    r"|(exists\.\s*please\s+try\s+logging\s+in)"
    r"|(sign\s*up\s+attempt\s+for\s+existing)"
    r"|(已被注册|已注册|账号已存在)",
    re.IGNORECASE,
)


def email_already_exists(resp: Response) -> bool:
    """
    判断这次注册是不是"邮箱已存在"。

    deploy.com 对已存在邮箱不返回 4xx：仍回 201，然后发一封
    "Sign Up Attempt for Existing Deploy Account / 已有账号存在，请去登录" 的信。
    我们看到的正是这封信。早先的注释"静默重发验证信或覆盖"是错的：
    它既不激活也不换密码，只会把账号状态搅乱，而且没注册的用户以后
    真的想登录时会拿不到可用会话。所以识别出来后必须**改走登录**。
    """
    if resp.status in (409, 422):
        return True
    body = resp.body or ""
    if not body:
        return False
    j = resp.json
    if isinstance(j, dict):
        parts = [str(j.get("message") or ""), str(j.get("errors") or ""), str(j.get("data") or "")]
        body = " ".join(parts) + " " + body
    return bool(ALREADY_EXISTS_RE.search(body))


# --------------------------------------------------------------------------
# API Key（入池用）
# --------------------------------------------------------------------------

# portal 创建 API Key 的默认授权模板（取自前端默认 JSON）
DEFAULT_STATEMENT = [{
    "key": "models",
    "action": ["hive:CallApi"],
    "effect": "Allow",
    "resource": ["hive:models:sf1:::v3/*", "hive:models:va1:::v3/*"],
}]


def create_api_key(http: "HttpClient", org_id: str | int) -> tuple[str, str, str]:
    """
    在 portal 里创建一把 API Key，返回 (access_key, secret_key, error)。

    ⚠️ Secret Key **只在创建时返回一次**，之后 GET /tokens 只给 access_key，
    所以拿到必须立刻落库，否则只能重开一把。

    实测：Deploy 的路由用 Secret Key 做 Authorization（不是 access_key）；
    access_key 只是门票 ID，用于编辑/删除。
    """
    # 先看有没有现成的（避免重复创建把门票堆满）
    lst = http.request("GET", f"{BASE}/api/organization/{org_id}/tokens")
    if lst.status == 200:
        try:
            rows = ((lst.json or {}).get("data") or [])
        except Exception:  # noqa: BLE001
            rows = []
        for r in rows:
            if isinstance(r, dict) and r.get("secret_key"):
                return r.get("access_key", ""), r["secret_key"], ""

    r = http.request("POST", f"{BASE}/api/organization/{org_id}/tokens",
                     {"statement": DEFAULT_STATEMENT})
    j = r.json or {}
    data = (j.get("data") or {}) if isinstance(j, dict) else {}
    ak = data.get("access_key") or ""
    sk = data.get("secret_key") or ""
    if r.status not in (200, 201) or not sk:
        msg = j.get("message") if isinstance(j, dict) else ""
        return "", "", f"创建 API Key 失败 HTTP {r.status}: {msg or r.body[:160]}"
    return ak, sk, ""


# --------------------------------------------------------------------------
# 主流程
# --------------------------------------------------------------------------

def build_client(item: dict, settings: dict, profile: Any = None) -> HttpClient:
    """按账号档案建 HTTP 客户端：指纹沿用注册时的那一套，出口沿用原代理。"""
    upstream = normalize_proxy(item.get("proxy") or "")
    bridge = settings.get("_bridge")
    proxy_endpoint = ""
    if upstream and bridge is not None:
        try:
            proxy_endpoint = bridge.endpoint_for(upstream)
        except Exception as e:  # noqa: BLE001
            emit("log", message=f"代理桥不可用，改直连: {e}")
    elif upstream:
        proxy_endpoint = upstream
    return HttpClient(
        timeout=30,
        transport=settings.get("transport", "curl_cffi"),
        profile=profile,
        proxy=proxy_endpoint or None,
    )


def find_hive_cookie(http: HttpClient) -> str:
    """
    取出会话 cookie。

    deploy 会在所有这些名字下下发同一会话（实测 hive-portal-production），
    逐个兜底，避免上游哪天换名导致"登录成功但 cookie 为空"。
    """
    for name in ("hive-portal-production", "hive-portal-production.sig",
                 "hive_portal_production", "session", "sessionid"):
        v = http.cookies.get(name, "")
        if v and not name.endswith(".sig"):
            return v
    for k, v in http.cookies.items():
        if "hive" in k.lower() and "portal" in k.lower() and not k.endswith(".sig"):
            return v
    return ""


def profile_view(http: HttpClient) -> dict | None:
    """GET /api/profile，成功返回归一化字段，失败返回 None。"""
    r = http.request("GET", PROFILE_URL)
    j = r.json or {}
    data = j.get("data") or {}
    if r.status != 200 or not isinstance(data, dict) or not data:
        emit("log", message=f"profile HTTP {r.status}: {j.get('message') or (r.body or '')[:120]}")
        return None
    return {
        "status": "verified" if data.get("is_verified") else "registered",
        "account_id": data.get("id"),
        "org_id": ((data.get("personal_organization") or {}).get("organization_id")),
        "is_verified": bool(data.get("is_verified")),
        "session_cookie": find_hive_cookie(http),
    }


def login_with_password(item: dict, settings: dict, profile: Any = None,
                        http: HttpClient | None = None,
                        password: str | None = None) -> dict | None:
    """
    走登录流程：GET /login?source=home-header → csrf → POST /api/login → profile。

    注册时把密码存下来（后端 Candidate.Password），账号已存在或会话过期时
    用同一个密码登回去，而不是再提交一次注册。
    """
    email = (item.get("email") or "").strip()
    password = (password or item.get("password") or "").strip()
    if not email or not password:
        emit("log", message="没有已保存的密码，无法走登录")
        return None
    if http is None:
        http = build_client(item, settings, profile)
    reg = Registrar(http, min_interval=2, max_retries=2)
    # 先打开真实登录页（用户指定入口），失败也不影响后续 API 登录
    try:
        lp = reg.login_page()
        if lp.status not in (200, 301, 302):
            emit("log", message=f"登录页 HTTP {lp.status}（继续 API 登录）")
    except Exception as e:  # noqa: BLE001
        emit("log", message=f"打开登录页失败（继续 API 登录）: {e}")
    lr = reg.login(email, password)
    j = lr.json or {}
    if lr.status != 200:
        emit("log", message=f"登录失败 HTTP {lr.status}: {j.get('message') or (lr.body or '')[:120]}")
        return None
    pv = profile_view(http)
    if pv is None:
        return None
    emit("log", message=f"密码登录成功 acct={pv['account_id']} verified={pv['is_verified']}")
    return {**pv, "reused": True, "relogin": True}


def resume_session(item: dict, settings: dict) -> dict | None:
    """
    不重新注册：先后用已保存的会话，会话失效则用已保存的密码走登录。

    顺序（实测 deploy 会话会被轮换/清理）：
      1. GET /api/profile + 保存的 hive-portal-production cookie → 200 即复用
      2. 401/403 → GET deploy.com/login?source=home-header + POST /api/login
      3. 登录也失败 → 返回 None，调用方决定是否重新注册（通常不注册：
         账号已存在时再注册会被上游当成"已存在账号重复注册"，只会多一封信）

    必须沿用注册时的 UA/TLS/代理：服务端虽不校验绑定，但换设备登录是自找风控。
    """
    cookie = (item.get("session_cookie") or "").strip()

    fp = load_fp_layer()
    profile = None
    if fp is not None:
        try:
            profile = fp.restore_profile(
                ua=item.get("ua", ""),
                browser=item.get("browser", ""),
                platform=item.get("platform", ""),
                ja3=item.get("ja3", ""),
            )
        except Exception as e:  # noqa: BLE001
            emit("log", message=f"还原指纹失败，用默认: {e}")

    http = build_client(item, settings, profile)

    if cookie:
        # 预置会话 cookie（原样，绝不能再 urlencode）
        http.cookies["hive-portal-production"] = cookie
        pv = profile_view(http)
        if pv is not None:
            # 服务端可能轮换 cookie，刷新保存值
            emit("log", message=f"会话复用成功 acct={pv['account_id']} verified={pv['is_verified']}")
            out = {**pv, "reused": True}
            if not out.get("session_cookie"):
                out["session_cookie"] = cookie
            return out
        emit("log", message="会话已失效，改走登录流程")
        http.cookies.pop("hive-portal-production", None)
    else:
        emit("log", message="没有保存的会话，直接走登录流程")

    return login_with_password(item, settings, profile, http=http)


def run_one(item: dict, settings: dict, emit_result: Callable[[dict], None]) -> dict:
    """
    处理一个候选：注册 -> 验证 -> 取会话。

    item 至少含: email, mailbox_url
    可含: password, name, company, phone
    """
    email = item["email"].strip()
    mailbox_url = item.get("mailbox_url", "").strip()
    password = item.get("password") or gen_password()
    # 重新认证接码：**不重新注册**，只从接码箱取一封已到达的验证信并验证。
    # deploy.com 没有可用的"重发验证"API（2026-11-13 实测 ajax-portal 下各种
    # 候选路径均 404，deploy.com/api/* 是 SPA 兜底），所以"重发"的正确做法
    # 是重新读已到达的信，而不是再提交一次注册。注册接口虽会对重复邮箱静默
    # 重发验证信，但那会覆盖账号状态、可能换掉临时密码，风险更大。
    resend_only = bool(item.get("resend_only"))
    resend_since = None
    if resend_only and item.get("resend_since"):
        try:
            resend_since = datetime.fromisoformat(
                re.sub(r"(\.\d{6})\d+", r"\1", str(item["resend_since"]).replace("Z", "+00:00"))
            ).timestamp()
        except Exception:  # noqa: BLE001
            resend_since = None

    # --- 0. 会话复用 / 登录：绝不重复注册已存在的账号 ---
    force_register = bool(settings.get("force_register"))
    if not resend_only and not force_register:
        # 有会话先校验会话，会话失效用保存的密码走登录；
        # 没有会话但有密码，同样先登录——只有登录明确说"账号不存在"
        # （或本来就没密码、没会话）才去注册。
        if item.get("session_cookie") or item.get("password"):
            resumed = resume_session(item, settings)
            if resumed:
                return {"email": email, "password": password, **resumed}
            if item.get("session_cookie") and not item.get("password"):
                log("会话失效且无已保存密码，无法走登录")
            elif item.get("password"):
                log("登录/会话都不可用，按新账号注册")
    min_interval = float(settings.get("min_interval", 8))
    max_retries = int(settings.get("max_retries", 6))
    verify_timeout = float(settings.get("verify_timeout", 240))
    transport = settings.get("transport", "curl_cffi")
    auto_verify = settings.get("auto_verify", True)
    upstream = normalize_proxy(settings.get("proxy") or "")
    sys_proxy = settings.get("sys_proxy") or "127.0.0.1:10808"

    result: dict[str, Any] = {"email": email, "password": password, "status": "failed"}

    # 指纹
    profile = pick_profile(seed=item.get("fp_seed"))
    if profile is not None:
        result.update({
            "ua": profile.ua, "browser": profile.browser, "platform": profile.platform,
            "ja3": profile.ja3 or "",
        })
        log(f"指纹: {profile.browser}/{profile.platform}")

    # 代理：经本地桥接（系统代理 -> 上游），curl_cffi 只看到本地 HTTP 代理
    proxy_endpoint = ""
    bridge = settings.get("_bridge")
    if upstream and bridge is not None:
        proxy_endpoint = bridge.endpoint_for(upstream)
        log(f"代理: {re.sub(r'//[^@]+@', '//***@', upstream)} via {proxy_endpoint}")
    elif upstream:
        proxy_endpoint = upstream
        log(f"代理: {re.sub(r'//[^@]+@', '//***@', upstream)}（无桥，直连）")

    http = HttpClient(timeout=30, transport=transport, profile=profile,
                      proxy=proxy_endpoint or None)
    reg = Registrar(http, min_interval=min_interval, max_retries=max_retries)

    # --- 接码准备：记录现有邮件 id ---
    reader = None
    before_ids: set = set()
    if mailbox_url:
        try:
            mb = Mailbox.parse(email, mailbox_url)
            reader = MailboxReader(mb)
            before_ids = reader.ids()
            log(f"接码就绪，现有 {len(before_ids)} 封")
        except Exception as e:  # noqa: BLE001
            log(f"接码地址无效: {e}")
            result["error"] = f"接码地址无效: {e}"
            return result

    # --- 1. 注册 ---
    if resend_only:
        result["status"] = "registered"
        if resend_since is None:
            resend_since = 0.0  # 没给基准就扫全箱，认领已到达的信
        log("重新认证接码：不重新注册，从接码箱取验证信" if not resend_since
            else f"重新认证接码：不重新注册，取 {resend_since:.0f} 之后到达的验证信")
        emit_result({**result})
    else:
        payload = build_payload(email, password, item.get("name"), item.get("company"), item.get("phone", ""))
        log(f"提交注册 {email}")
        resp = reg.register(payload)
        j = resp.json or {}
        if email_already_exists(resp):
            # 上游对已存在邮箱回 201，但只发"已有账号，请去登录"的信（见截图）。
            # 这时候继续等验证信是白等，直接拿保存的密码走登录。
            log("账号已存在（注册返回已存在），改走登录流程，不再等待验证信")
            resumed = login_with_password(item, settings, profile, http=http, password=password)
            if resumed:
                return {"email": email, "password": password, **resumed}
            result["status"] = "registered"
            result["error"] = "账号已存在于上游，但保存的密码未能登录（可能密码不匹配）"
            result["detail"] = "already_exists"
            emit("log", message=result["error"])
            return result
        if resp.status != 201:
            result["error"] = f"注册失败 HTTP {resp.status}: {j.get('message') or resp.body[:160]}"
            emit("log", message=result["error"])
            return result
        result["status"] = "registered"
        log("注册成功 201")
        emit_result({**result})  # 中间态，先落库
        # Deploy 的验证信可能晚一小时以上才到。以**收到 201 的时刻**为基准，
        # 之后到达的验证信才算这次注册的（旧信不会误抓）。
        resend_since = time.time()

    # --- 2. 取验证链接 ---
    if not (auto_verify and reader):
        result["detail"] = "未启用自动验证"
        return result

    got = reader.wait_for_link(before_ids, timeout=verify_timeout, since_ts=resend_since)
    if not got:
        result["error"] = f"{verify_timeout:.0f}s 内未收到验证邮件"
        emit("log", message=result["error"])
        return result
    token, mail = got
    result["verify_email_id"] = mail.get("id")
    log(f"拿到验证 token ({len(token)} chars)")

    # --- 3. 验证 ---
    ar = reg.authenticate(token)
    aj = ar.json or {}
    if ar.status != 200:
        result["error"] = f"验证失败 HTTP {ar.status}: {aj.get('message')}"
        emit("log", message=result["error"])
        # 兜底：走登录
    data = aj.get("data") or {}
    result["account_id"] = data.get("id")
    result["verified"] = True
    log(f"验证通过 account_id={data.get('id')}")

    # --- 4. 确认会话（拿不到就登录）---
    pr = reg.profile()
    pj = (pr.json or {}).get("data") or {}
    if pr.status == 200:
        result["is_verified"] = bool(pj.get("is_verified"))
        result["org_id"] = ((pj.get("personal_organization") or {}).get("organization_id"))
        result["default_org_id"] = pj.get("default_organization_id") or pj.get("default_organization")
        log(f"profile OK is_verified={result['is_verified']} org={result['org_id']}")
    else:
        # 没拿到会话，用密码登录补一次
        lr = reg.login(email, password)
        log(f"profile {pr.status}，改用登录: {lr.status}")
        if lr.status == 200:
            pr2 = reg.profile()
            pj2 = (pr2.json or {}).get("data") or {}
            result["is_verified"] = bool(pj2.get("is_verified"))
            result["org_id"] = ((pj2.get("personal_organization") or {}).get("organization_id"))
            result["account_id"] = pj2.get("id") or result.get("account_id")

    # 会话保存：服务端下发/轮换后的最终值，后端会原样落库，下次直接复用
    result["session_cookie"] = find_hive_cookie(http) or result.get("session_cookie", "")

    # --- 5. 需要入池时，顺手创建 API Key（Secret Key 只返回一次）---
    if settings.get("import_to_pool") and result.get("org_id"):
        ak, sk, kerr = create_api_key(http, result["org_id"])
        if sk:
            result["access_key"] = ak
            result["secret_key"] = sk
            log(f"API Key 已创建 access_key={ak}")
        else:
            result["api_key_error"] = kerr
            log(f"API Key 创建失败（不影响账号可用）: {kerr}")

    if profile is not None:
        # 记录实际使用的浏览器底座，复用会话时据此还原 TLS
        result["browser"] = profile.browser
        result["platform"] = profile.platform
        result["ua"] = profile.ua
        result["ja3"] = profile.ja3 or ""
        result["fp_impersonate"] = profile.impersonate
    result["status"] = "verified" if result.get("is_verified") else "registered"
    return result


def cmd_job(job_path: str) -> int:
    """后端调用入口：读 job.json，逐条处理，每步 emit。"""
    job = json.loads(Path(job_path).read_text(encoding="utf-8"))
    settings = job.get("settings") or {}
    items = job.get("items") or []
    emit("start", count=len(items))

    # 代理桥：整个批次共用一个管理器，按账号轮询复用
    bridge = None
    if any((it.get("proxy") or settings.get("proxy")) for it in items):
        bridge = ProxyBridge(
            sys_proxy=settings.get("sys_proxy") or "127.0.0.1:10808",
            base_port=int(settings.get("bridge_port") or 17500),
        )
        settings["_bridge"] = bridge
        emit("log", message=f"代理桥已启用（系统代理 {bridge.sys_proxy}）")

    for idx, item in enumerate(items):
        emit("item_start", index=idx, email=item.get("email"))
        try:
            res = run_one(item, settings, lambda r: emit("item_update", index=idx, **r))
        except Exception as e:  # noqa: BLE001
            res = {"email": item.get("email"), "status": "failed",
                   "error": f"{type(e).__name__}: {e}"}
        res["index"] = idx
        emit("item_done", **res)
        # 条间隔，避免连续吃限流
        if idx < len(items) - 1:
            gap = float(settings.get("min_interval", 8)) + random.uniform(2, 6)
            emit("log", message=f"下一条 {gap:.0f}s 后开始")
            time.sleep(gap)

    if bridge is not None:
        bridge.close()
    emit("done", count=len(items))
    return 0


# --------------------------------------------------------------------------
# CLI
# --------------------------------------------------------------------------

def cmd_batch(args: argparse.Namespace) -> int:
    """独立批量：从 --file 读 email----mailbox_url 行。"""
    items: list[dict] = []
    raw = Path(args.file).read_text(encoding="utf-8")
    for line in raw.splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        if "----" in line:
            email, url = line.split("----", 1)
        elif "\t" in line:
            email, url = line.split("\t", 1)
        else:
            continue
        items.append({"email": email.strip(), "mailbox_url": url.strip()})

    if args.limit:
        items = items[: args.limit]

    settings = {
        "min_interval": args.min_interval,
        "max_retries": args.max_retries,
        "verify_timeout": args.verify_timeout,
        "transport": args.transport,
        "auto_verify": not args.no_verify,
    }
    proxies = []
    if args.proxy_file and Path(args.proxy_file).exists():
        proxies = [l.strip() for l in Path(args.proxy_file).read_text().splitlines()
                   if l.strip() and not l.startswith("#")]

    emit("start", count=len(items))
    bridge = None
    if proxies:
        bridge = ProxyBridge(sys_proxy=args.sys_proxy, base_port=args.bridge_port)
        settings["_bridge"] = bridge
        emit("log", message=f"代理桥已启用（系统代理 {args.sys_proxy}，上游 {len(proxies)} 个）")
    out = open(args.out, "a", encoding="utf-8") if args.out else None
    ok = fail = 0
    for i, it in enumerate(items):
        st = dict(settings)
        if proxies:
            st["proxy"] = pick_proxy(proxies, i)  # 轮询复用
        emit("item_start", index=i, email=it["email"])
        try:
            res = run_one(it, st, lambda r: emit("item_update", index=i, **r))
        except Exception as e:  # noqa: BLE001
            res = {"email": it["email"], "status": "failed", "error": f"{type(e).__name__}: {e}"}
        res["index"] = i
        if res.get("status") in ("registered", "verified"):
            ok += 1
        else:
            fail += 1
        emit("item_done", **res)
        if out:
            out.write(json.dumps(res, ensure_ascii=False) + "\n")
            out.flush()
        if i < len(items) - 1:
            time.sleep(args.min_interval + random.uniform(2, 6))

    if out:
        out.close()
    if bridge is not None:
        bridge.close()
    emit("done", count=len(items), ok=ok, failed=fail)
    print(f"\n完成: ok={ok} failed={fail}", file=sys.stderr)
    return 0


def cmd_probe_proxy() -> int:
    """
    探测代理能否出网，输出 {"ip": ...} 或 {"error": ...}。

    链路与注册完全一致：本地桥 → 系统代理 → 上游代理 → api.ip.sb，
    这样"上游能连但出不去"也能被测出来。
    """
    upstream = os.environ.get("DEPLOY_PROBE_UPSTREAM", "").strip()
    sys_proxy = os.environ.get("DEPLOY_PROBE_SYS", "").strip()
    # 探测模式下日志走 stderr，stdout 只留最后那一行 JSON，供后端解析
    globals()["emit"] = lambda kind, **kw: print(
        f"[{kind}] {kw.get('message', '')}", file=sys.stderr)
    bridge = None
    try:
        proxies = None
        if upstream:
            bridge = ProxyBridge(sys_proxy=sys_proxy, base_port=17600)
            ep = bridge.endpoint_for(upstream)
            proxies = {"https": ep, "http": ep}
        elif sys_proxy:
            proxies = {"https": f"http://{sys_proxy}", "http": f"http://{sys_proxy}"}
        kwargs = dict(timeout=30, impersonate="chrome")
        if proxies:
            kwargs["proxies"] = proxies
        from curl_cffi import requests as _cr
        r = _cr.get("https://api.ip.sb/ip", **kwargs)
        ip = (r.text or "").strip()
        if not ip or len(ip) > 64:
            print(json.dumps({"error": f"返回异常: {ip[:60]}"}, ensure_ascii=False))
            return 1
        print(json.dumps({"ip": ip}, ensure_ascii=False))
        return 0
    except Exception as e:  # noqa: BLE001
        print(json.dumps({"error": f"{type(e).__name__}: {e}"[:160]}, ensure_ascii=False))
        return 1
    finally:
        if bridge is not None:
            bridge.close()


def main(argv: Iterable[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description="deploy.com 注册机")
    ap.add_argument("--job-file", help="后端传入的 job.json")
    ap.add_argument("--file", help="批量文件，格式 email----mailbox_url")
    ap.add_argument("--out", default=None, help="结果 jsonl")
    ap.add_argument("--limit", type=int, default=0)
    ap.add_argument("--proxy-file", default=None)
    ap.add_argument("--sys-proxy", default="127.0.0.1:10808",
                    help="本机系统代理（Clash 等），上游代理经它出网；留空表示不用")
    ap.add_argument("--bridge-port", type=int, default=17500, help="本地代理桥起始端口")
    ap.add_argument("--transport", default="curl_cffi", choices=["curl_cffi", "urllib"])
    ap.add_argument("--min-interval", type=float, default=8.0)
    ap.add_argument("--max-retries", type=int, default=6)
    ap.add_argument("--verify-timeout", type=float, default=240.0)
    ap.add_argument("--no-verify", action="store_true", help="只注册，不取信验证")
    ap.add_argument("--job", action="store_true", help="从 stdin 读 job.json")
    ap.add_argument("--probe-proxy", action="store_true",
                    help="只探测代理出口 IP（环境变量 DEPLOY_PROBE_UPSTREAM / DEPLOY_PROBE_SYS）")
    args = ap.parse_args(argv)

    if args.probe_proxy:
        return cmd_probe_proxy()

    if args.job_file:
        return cmd_job(args.job_file)
    if args.job:
        import tempfile

        raw = sys.stdin.read()
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False, encoding="utf-8") as f:
            f.write(raw)
            path = f.name
        try:
            return cmd_job(path)
        finally:
            os.unlink(path)
    if args.file:
        return cmd_batch(args)
    ap.print_help()
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
