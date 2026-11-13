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

    def wait_for_link(self, after_ids: set, timeout: float = 240.0, poll: float = 5.0) -> tuple[str, dict] | None:
        """等待新邮件里的 deploy verify token，返回 (token, email_obj)。"""
        deadline = time.time() + timeout
        while time.time() < deadline:
            try:
                mails = self.fetch("all")
            except Exception as e:  # noqa: BLE001
                emit("log", message=f"取信失败，重试: {e}")
                time.sleep(poll)
                continue
            for e in mails:
                if e.get("id") in after_ids:
                    continue
                blob = (e.get("textContent") or "") + " " + (e.get("htmlContent") or "")
                if "deploy.com" not in blob and "deploy" not in (e.get("fromAddress") or "").lower():
                    continue
                m = re.search(r"https://deploy\.com/verify-email\?token=([A-Za-z0-9_\-]+)", blob)
                if m:
                    return m.group(1), e
            remain = int(deadline - time.time())
            emit("log", message=f"等待验证邮件… 剩余 {remain}s")
            time.sleep(poll)
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
        return self.http.request("POST", f"{LOGIN_URL}?enforce_csrf=true", payload)


# --------------------------------------------------------------------------
# 主流程
# --------------------------------------------------------------------------

def resume_session(item: dict, settings: dict) -> dict | None:
    """
    复用已保存的会话，不重新注册。

    这正是「deploy 登录会话必须保存」的用途：
      - 号池里的账号已有 hive-portal-production cookie
      - 直接 GET /api/profile 校验，成功即视为可用
      - **必须**沿用注册时的 UA/TLS 指纹，否则等于换了一台设备登录

    返回校验后的结果 dict；会话失效返回 None（调用方回落去登录）。
    """
    cookie = (item.get("session_cookie") or "").strip()
    if not cookie:
        return None

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

    upstream = normalize_proxy(item.get("proxy") or "")
    bridge = settings.get("_bridge")
    proxy_endpoint = ""
    if upstream and bridge is not None:
        try:
            proxy_endpoint = bridge.endpoint_for(upstream)
        except Exception as e:  # noqa: BLE001
            emit("log", message=f"代理桥不可用，改直连: {e}")

    http = HttpClient(
        timeout=30,
        transport=settings.get("transport", "curl_cffi"),
        profile=profile,
        proxy=proxy_endpoint or None,
    )
    # 预置会话 cookie（原样，绝不能再 urlencode）
    http.cookies["hive-portal-production"] = cookie

    r = http.request("GET", PROFILE_URL)
    j = r.json or {}
    data = j.get("data") or {}
    if r.status == 200 and data.get("email"):
        org = ((data.get("personal_organization") or {}).get("organization_id"))
        emit("log", message=f"会话复用成功 acct={data.get('id')} verified={data.get('is_verified')}")
        # 服务端可能轮换 cookie，刷新保存值
        fresh = http.cookies.get("hive-portal-production", "")
        return {
            "status": "verified" if data.get("is_verified") else "registered",
            "account_id": data.get("id"),
            "org_id": org,
            "is_verified": bool(data.get("is_verified")),
            "session_cookie": fresh or cookie,
            "reused": True,
        }
    emit("log", message=f"会话已失效（HTTP {r.status}: {j.get('message') or ''}），尝试密码登录")
    password = (item.get("password") or "").strip()
    if not password:
        emit("log", message="无已保存密码，无法登录，将重新注册")
        return None

    reg = Registrar(http, min_interval=2, max_retries=2)
    lr = reg.login(item["email"], password)
    if lr.status != 200:
        emit("log", message=f"登录失败 HTTP {lr.status}: {(lr.json or {}).get('message') or ''}，将重新注册")
        return None

    pr = http.request("GET", PROFILE_URL)
    pj = (pr.json or {}).get("data") or {}
    org = ((pj.get("personal_organization") or {}).get("organization_id"))
    emit("log", message=f"密码登录成功 acct={pj.get('id')} verified={pj.get('is_verified')}")
    return {
        "status": "verified" if pj.get("is_verified") else "registered",
        "account_id": pj.get("id"),
        "org_id": org,
        "is_verified": bool(pj.get("is_verified")),
        "session_cookie": http.cookies.get("hive-portal-production", ""),
        "reused": True,
        "relogin": True,
    }


def run_one(item: dict, settings: dict, emit_result: Callable[[dict], None]) -> dict:
    """
    处理一个候选：注册 -> 验证 -> 取会话。

    item 至少含: email, mailbox_url
    可含: password, name, company, phone
    """
    email = item["email"].strip()
    mailbox_url = item.get("mailbox_url", "").strip()
    password = item.get("password") or gen_password()

    # --- 0. 会话复用：已有有效会话就直接返回，不重复注册 ---
    if item.get("session_cookie") and not settings.get("force_register"):
        resumed = resume_session(item, settings)
        if resumed:
            return {"email": email, "password": password, **resumed}
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
    payload = build_payload(email, password, item.get("name"), item.get("company"), item.get("phone", ""))
    log(f"提交注册 {email}")
    resp = reg.register(payload)
    j = resp.json or {}
    if resp.status != 201:
        result["error"] = f"注册失败 HTTP {resp.status}: {j.get('message') or resp.body[:160]}"
        emit("log", message=result["error"])
        return result
    result["status"] = "registered"
    log("注册成功 201")
    emit_result({**result})  # 中间态，先落库

    # --- 2. 取验证链接 ---
    if not (auto_verify and reader):
        result["detail"] = "未启用自动验证"
        return result

    got = reader.wait_for_link(before_ids, timeout=verify_timeout)
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

    # --- 4. 确认会话 ---
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

    result["session_cookie"] = http.cookies.get("hive-portal-production", "")
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
    args = ap.parse_args(argv)

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
