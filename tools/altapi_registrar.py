#!/usr/bin/env python3
"""
altapi.ai (TokenPower / new-api) 注册机 —— 接码 + 注册 + 登录 + 生成 API Key

被 account-pool-balancer 后端以子进程方式调用，也支持独立 CLI。

完整流程（2026-11-15 Chrome CDP 抓包实测）:
  1. GET  /api/status                       确认 turnstile_check=false
  2. GET  /api/verification?email=&turnstile=      发码（Resend，按邮箱 30s 限流）
  3. GET  <mida>/api/messages?alias_email=&token=&range=latest   取验证码
  4. POST /api/user/register?turnstile=     注册
  5. POST /api/user/login?turnstile=        登录 -> 拿 session cookie
  6. GET  /api/user/self                    确认身份 + 读 quota
  7. POST /api/token/                       建 token
  8. GET  /api/token/?p=1&size=100          找 id
  9. POST /api/token/{id}/key               取明文 key

关键契约（全部实测，踩过的坑都在这里）:
  - 认证是 session cookie（httpOnly/Lax/30d）+ New-API-User header，不是 JWT
  - 验证码是 6 位十六进制（如 48890d），不是 6 位数字
  - 明文 key 必须 POST /api/token/{id}/key，GET 会掉进网关报 Invalid URL
  - 新号赠送 quota=5000000，quota_per_unit=500000，即 $10.00

后端调用约定:
  python3 altapi_registrar.py --job-file /path/job.json
  stdout 每行一个 JSON 事件。

用法:
  altapi_registrar.py --job-file job.json
  altapi_registrar.py --email a@b.c --mailbox 'https://...' --proxy 'socks5h://...'
  altapi_registrar.py --probe            # 只看站点状态
"""

from __future__ import annotations

import argparse
import json
import os
import random
import re
import string
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Callable

HERE = Path(__file__).resolve().parent

# --------------------------------------------------------------------------
# altapi.ai 契约（实测 2026-11-15）
# --------------------------------------------------------------------------

DEFAULT_BASE = "https://altapi.ai"
DEFAULT_SEND_GAP = 32.0          # 同邮箱 30s，留 2s 余量
DEFAULT_VERIFY_TIMEOUT = 180.0
# 收件箱轮询间隔随机化：固定 4.0 秒会让所有账号呈现同一节拍，
# 收信服务侧（以及任何观察流量的地方）一看就是脚本。
POLL_LO, POLL_HI = 3.0, 9.0


def poll_sleep() -> None:
    """按随机间隔等下一次查信。"""
    time.sleep(random.uniform(POLL_LO, POLL_HI))

# ---------------------------------------------------------------------------
# 步骤间节奏（human_pace）
#
# 站点没有 Turnstile，靠行为与频率识别脚本。纯请求体层面没有任何挑战，但
# 「提交注册」后 80ms 就登录、登录后 50ms 就读 /api/user/self、再 50ms 就建
# Token —— 这个时序人类做不出来，是最容易被打标的地方。
#
# 每个核心步骤之间插一段范围随机的停顿，模拟真实用户在页面上的操作间隔：
#   发码 → （等邮件，天然有间隔）→ 提交注册 → 登录 → 读身份 → 建 Key
#
# 上限不宜过大：注册一次本来 8~20 秒，多等 3~6 秒对吞吐影响有限，但对
# 「像不像人」帮助明显。需要更快可把下面调到 0（不推荐）。
PACE_STEP = (2.0, 4.5)      # 相邻核心步骤之间的间隔
PACE_AFTER_REGISTER = (3.0, 6.0)   # 注册成功后到登录前，用户通常会看一眼结果页
PACE_AFTER_LOGIN = (1.5, 3.0)      # 登录后到读身份/建 Key，页面渲染需要时间
# 收到验证码 → 提交注册。这是整条链路里最像机器人的瞬间：脚本是「信一到就
# 立刻填」。真人要切窗口、找信、复制、切回、粘贴、再点提交，通常十几秒到
# 一分多钟，且时长很分散。这个区间比其它步骤明显长，并且额外做一次长尾抽样。
PACE_AFTER_CODE = (9.0, 28.0)


# 当前 job 的节奏系数：1.0 = 默认，0 = 关闭（不推荐），2.0 = 更慢更稳。
_pace_scale = 1.0
# 收码后停顿下限，来自设置页 code_delay_sec；0 表示用默认区间。
_code_delay_lo = 0.0


def set_code_delay(lo: float) -> None:
    global _code_delay_lo
    try:
        _code_delay_lo = max(0.0, float(lo))
    except (TypeError, ValueError):
        _code_delay_lo = 0.0


def set_pace_scale(v: float) -> None:
    """从 job settings 的 pac_scale 设置节奏倍数。"""
    global _pace_scale
    try:
        _pace_scale = max(0.0, float(v))
    except (TypeError, ValueError):
        _pace_scale = 1.0


def pace_after_code() -> float:
    """收码后的停顿：区间来自设置（code_delay_sec 为下限），否则用默认。

    单独一个函数是为了让运维能在设置页把「等多久才填验证码」调到符合自己
    业务观感的水平，而不用改代码。
    """
    lo, hi = PACE_AFTER_CODE
    if _code_delay_lo > 0:
        # 设置值作为中心，区间取 [0.7x, 1.5x]，保留随机性但不无限放大。
        lo = max(1.0, _code_delay_lo * 0.7)
        hi = max(lo + 1.0, _code_delay_lo * 1.5)
    return pace((lo, hi))


def pace(span: tuple[float, float], why: str = "") -> float:
    """睡一段 span 内均匀随机的时间，返回实际睡了多久。

    随机而不是固定值，避免所有账号出现完全一致的时间特征；再乘上
    pace_scale（来自设置页），让运维能整体调快调慢。
    """
    lo, hi = span
    if hi <= 0 or _pace_scale <= 0:
        return 0.0
    secs = random.uniform(lo, hi) * _pace_scale
    time.sleep(secs)
    return secs
MAILBOX_API = "/api/messages"

# 指纹：UA 必须与 sec-ch-ua / sec-ch-ua-platform / Accept-Language 自洽，
# 否则 CF 能靠这一组矛盾把多个账号关联到同一脚本。
FP_PROFILES = [
    {
        # impersonate 决定 TLS/HTTP2 指纹（ClientHello 的 cipher 顺序、曲线、
        # 扩展集合、GREASE、ALPN），这些是站点第一眼看到的东西，UA 只能排第二。
        # UA 与 impersonate 必须同版本：Chrome 141 的 UA 配 Chrome 136 的
        # ClientHello 是自相矛盾的指纹。
        "impersonate": "chrome",
        "ua": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
              "(KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36",
        "ch_ua": '"Google Chrome";v="141", "Chromium";v="141", "Not?A_Brand";v="24"',
        "platform": '"Windows"',
        "lang": "en-US,en;q=0.9",
        "gpc": True,
    },
    {
        "impersonate": "chrome",
        "ua": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 "
              "(KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36",
        "ch_ua": '"Chromium";v="142", "Google Chrome";v="142", "Not?A_Brand";v="99"',
        "platform": '"macOS"',
        "lang": "en-US,en;q=0.9",
        "gpc": True,
    },
    {
        "impersonate": "chrome",
        "ua": "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "
              "(KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36",
        "ch_ua": '"Google Chrome";v="143", "Chromium";v="143", "Not?A_Brand";v="24"',
        "platform": '"Linux"',
        "lang": "en-GB,en;q=0.9",
        "gpc": True,
    },
]

# --------------------------------------------------------------------------
# 事件输出
# --------------------------------------------------------------------------

class Emitter:
    """stdout 一行一个 JSON 事件，供后端 bufio.Scanner 消费。"""

    def __init__(self, index: int = 0, email: str = ""):
        self.index = index
        self.email = email

    def emit(self, typ: str, **kw: Any) -> None:
        ev = {"type": typ, "index": self.index, "email": self.email}
        ev.update({k: v for k, v in kw.items() if v is not None})
        sys.stdout.write(json.dumps(ev, ensure_ascii=False) + "\n")
        sys.stdout.flush()

    def log(self, msg: str) -> None:
        self.emit("log", message=msg)

    def update(self, **kw: Any) -> None:
        self.emit("item_update", **kw)

    def done(self, status: str, **kw: Any) -> None:
        self.emit("item_done", status=status, **kw)


# --------------------------------------------------------------------------
# HTTP
# --------------------------------------------------------------------------

@dataclass
class Reply:
    status: int
    text: str
    headers: dict = field(default_factory=dict)

    def json(self) -> Any:
        try:
            return json.loads(self.text)
        except Exception:
            return None

    @property
    def cf_blocked(self) -> bool:
        return self.status in (403, 429, 503) or not self.text.strip()


class Transport:
    """把「发一个请求」和「指纹由谁决定」分开。

    实测 2026-11-16（同一代理出口、同一时刻）：
        Python urllib + 完美 Chrome UA/CH 头  → 连接被 Cloudflare 直接断开
        curl_cffi impersonate=chrome          → HTTP/3 200
    说明 UA 和 sec-ch-ua 再怎么对，也救不了 TLS 层。Python 走的是系统
    LibreSSL，ClientHello 的 cipher 顺序、曲线、扩展集合、GREASE 全和
    BoringSSL 不同 —— 这就是 JA3/JA4 的原料，是站点第一眼看到的东西。

    所以传输层**默认必须**用 curl_cffi；urllib 只在装了库的降级路径上，
    并且必须把降级说出来，否则会以「UA 明明是对的却被拦」的形式误导排查。
    """

    def __init__(self, proxy: str | None, impersonate: str = "chrome"):
        self.proxy = proxy
        self.impersonate = impersonate
        self.mode = ""
        self._cffi = None
        self._urllib = None
        self._init()

    def _init(self) -> None:
        try:
            from curl_cffi import requests as cr  # noqa: F401
            self._cffi = cr
            self.mode = "curl_cffi/" + self.impersonate
            return
        except ImportError:
            pass
        self._urllib = _urllib_opener(self.proxy)
        self.mode = "urllib/LibreSSL"

    @property
    def tls_is_browserlike(self) -> bool:
        return self._cffi is not None

    # 导航形态的残留头，必须在 XHR 场景里摘掉
    _NAV_ONLY = ("sec-fetch-user", "upgrade-insecure-requests", "priority")

    def request(self, method: str, url: str, headers: dict,
                body: bytes | None, timeout: float):
        """返回 (status, text, set_cookies)。"""
        proxies = {}
        if self.proxy:
            p = remote_dns_proxy(self.proxy)
            proxies = {"http": p, "https": p}
        # curl_cffi 的 impersonate 默认套用「顶层导航」形态：
        #   Sec-Fetch-Dest: document / Mode: navigate / Site: none
        #   Sec-Fetch-User: ?1 / Upgrade-Insecure-Requests: 1 / Priority
        # 我们的每个请求都是页面内的 XHR，真实浏览器此时发的是
        #   Dest: empty / Mode: cors / Site: same-origin，且没有后三个。
        # 带着导航头等于自报「我不是页面加载的请求」。
        # 用 None 才能真的摘掉（空串会退化成去掉头部值但仍保留该头）。
        if self._cffi is not None:
            send = dict(headers)
            for k in self._NAV_ONLY:
                send[k] = None
            try:
                r = self._cffi.request(
                    method, url, headers=send, data=body,
                    impersonate=self.impersonate, proxies=proxies or None,
                    timeout=timeout, allow_redirects=False,
                    verify=True,
                )
            except TypeError:
                # 老版本 curl_cffi 不认 allow_redirects 之外的部分参数
                r = self._cffi.request(method, url, headers=send, data=body,
                                       impersonate=self.impersonate,
                                       proxies=proxies or None, timeout=timeout)
            cookies = []
            for k, v in (r.cookies.items() if hasattr(r, "cookies") else []):
                cookies.append(f"{k}={v}")
            return r.status_code, r.text, cookies
        return _urllib_request(self._urllib, method, url, headers, body, timeout)


def remote_dns_proxy(proxy: str) -> str:
    """把 SOCKS 代理 URL 规范成「由代理远端解析域名」的形态。

    socks5:// 在 libcurl/PySocks 语义里是**本地**解析 DNS，再让代理去连 IP。
    对 altapi.ai 这种 Cloudflare 前置的站点，实测（2026-11-16，同一代理）：
        socks5://   → curl (35) SSL_connect: Connection closed abruptly
        socks5h://  → 200
    代理按裸 IP 连出去的 TLS 会话被 CF 直接掐断。Go 侧 balancer 的 SOCKS
    拨号器一直是把主机名交给代理解析，所以线上推理没问题；注册机必须和它
    一致，否则同一个代理在推理里能用、在注册里报 TLS 错，排查会被带偏。
    http/https 代理不受影响，原样返回。
    """
    p = proxy.strip()
    low = p.lower()
    if low.startswith("socks5://"):
        return "socks5h://" + p[len("socks5://"):]
    if low.startswith("socks4://"):
        return "socks4a://" + p[len("socks4://"):]
    return p


def _urllib_opener(proxy: str | None):
    if not proxy:
        # 明确不读环境代理，避免宿主 Clash 把流量带走导致出口不可控
        return urllib.request.build_opener(urllib.request.ProxyHandler({}))
    if proxy.startswith("socks"):
        try:
            import socks  # noqa: F401
            import socket
            from urllib.parse import urlparse

            u = urlparse(remote_dns_proxy(proxy))
            scheme = socks.SOCKS5 if (u.scheme or "").startswith("socks5") else socks.SOCKS4
            # 经 remote_dns_proxy 之后 socks 一律远端解析，见该函数说明
            rdns = (u.scheme or "") in ("socks5h", "socks4a")
            socks.set_default_proxy(
                scheme, u.hostname, u.port or 1080,
                username=urllib.parse.unquote(u.username or "") or None,
                password=urllib.parse.unquote(u.password or "") or None,
                rdns=rdns,
            )
            socket.socket = socks.socksocket
            return urllib.request.build_opener(urllib.request.ProxyHandler({}))
        except ImportError:
            # 这条错误在部署里出现过全量失败：镜像 venv 装了 PySocks，但设置里
            # python_bin 被写成裸 "python3"，于是走了系统解释器。把实际用的
            # 解释器打出来，下次一眼能看出走错了哪个。
            raise RuntimeError(
                f"socks 代理需要 PySocks，但当前解释器 {sys.executable} 没装；"
                f"检查设置页「注册机解释器」是否指向装好依赖的 venv（或留空用镜像默认）")
    return urllib.request.build_opener(
        urllib.request.ProxyHandler({"http": proxy, "https": proxy}))


def _urllib_request(opener, method, url, headers, body, timeout):
    req = urllib.request.Request(url, data=body, method=method, headers=headers)
    try:
        with opener.open(req, timeout=timeout) as r:
            return r.status, r.read().decode("utf-8", "replace"), \
                   list(r.headers.get_all("Set-Cookie") or [])
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode("utf-8", "replace"), \
               list(e.headers.get_all("Set-Cookie") or [])
    except Exception as e:
        return 0, f"__transport__ {e}", []


class HTTP:
    def __init__(self, base: str, fp: dict, proxy: str | None = None):
        self.base = base.rstrip("/")
        self.fp = fp
        self.transport = Transport(proxy, impersonate=fp.get("impersonate", "chrome"))
        self.cookies: dict[str, str] = {}
        # uid is the logged-in user id. new-api requires New-Api-User to equal
        # it: "-1" answers 401 "与登录用户不匹配" and a missing header answers
        # 401 "未提供 New-Api-User". It stays empty until login returns it.
        #
        # This mirrors internal/newapiconsole (the Go side); the rules are
        # duplicated here only because the registrar must not depend on the
        # Go binary. Keep them identical.
        self.uid: str = ""

    def headers(self, referer: str = "/", extra: dict | None = None) -> dict:
        h = {
            "User-Agent": self.fp["ua"],
            "Accept": "*/*",
            "Accept-Language": self.fp["lang"],
            "sec-ch-ua": self.fp["ch_ua"],
            "sec-ch-ua-mobile": "?0",
            "sec-ch-ua-platform": self.fp["platform"],
            "sec-fetch-dest": "empty",
            "sec-fetch-mode": "cors",
            "sec-fetch-site": "same-origin",
            # Accept-Encoding 由传输层决定（curl_cffi 发 gzip, deflate, br, zstd；
            # urllib 只能发 identity）。在这里手写会与实际编码能力矛盾：
            # 声称支持 br 却解不开，响应体直接坏掉。
            "New-Api-User": normalize_uid(self.uid) or "-1",
            "Origin": self.base,
            "Referer": self.base + referer,
        }
        # 真实 Chrome 的 XHR 还带这几个；缺了它们，头部集合比浏览器「瘦」，
        # 是和 TLS 指纹独立的第二道可识别特征。
        if self.fp.get("gpc"):
            h["sec-gpc"] = "1"
        if self.cookies:
            h["Cookie"] = "; ".join(f"{k}={v}" for k, v in self.cookies.items())
        if extra:
            h.update(extra)
        return h

    def _absorb(self, hdrs) -> None:
        for raw in hdrs.get_all("Set-Cookie") or []:
            pair = raw.split(";", 1)[0]
            if "=" in pair:
                k, v = pair.split("=", 1)
                self.cookies[k.strip()] = v.strip()

    def call(self, method: str, path: str, referer: str = "/",
             body: Any = None, timeout: float = 40.0) -> Reply:
        data = None
        extra = {}
        if body is not None:
            data = json.dumps(body).encode()
            extra["Content-Type"] = "application/json"
        status, text, set_cookies = self.transport.request(
            method, self.base + path, self.headers(referer, extra), data, timeout)
        for raw in set_cookies:
            pair = raw.split(";", 1)[0]
            if "=" in pair:
                k, v = pair.split("=", 1)
                self.cookies[k.strip()] = v.strip()
        return Reply(status, text, {})

    def get(self, path: str, **kw) -> Reply:
        return self.call("GET", path, **kw)

    def post(self, path: str, **kw) -> Reply:
        return self.call("POST", path, **kw)


# --------------------------------------------------------------------------
# 接码：icloud.mida.vip 分享链接
# --------------------------------------------------------------------------

class Mailbox:
    """接码地址形如 https://icloud.mida.vip/messages/<email>?token=<token>。

    真实 API: GET /api/messages?alias_email=&token=&range=latest|3d|all
    响应里的 verificationCode 由服务端直接抽好 —— 但要注意 altapi 的验证码是
    6 位十六进制，服务端正则能抓对；万一为空则回落到正文自解析。
    """

    def __init__(self, share_url: str, proxy: str | None = None):
        u = urllib.parse.urlparse(share_url)
        q = urllib.parse.parse_qs(u.query)
        self.base = f"{u.scheme}://{u.netloc}"
        self.token = (q.get("token") or [""])[0]
        self.alias = urllib.parse.unquote(u.path.rsplit("/", 1)[-1])
        # 收码站不是 Cloudflare 保护的目标，用哪个栈都行；但统一走 Transport
        # 可以少一份代理/超时实现，也避免 socks monkey-patch 重复生效。
        self.transport = Transport(proxy, impersonate="chrome")

    def _fetch(self, rng: str = "latest") -> list[dict]:
        q = urllib.parse.urlencode(
            {"alias_email": self.alias, "token": self.token, "range": rng})
        status, text, _ = self.transport.request(
            "GET", f"{self.base}{MAILBOX_API}?{q}",
            {"Accept": "application/json", "User-Agent": FP_PROFILES[0]["ua"]},
            None, 30)
        try:
            d = json.loads(text)
        except Exception:
            return []
        if not d.get("ok"):
            return []
        return d.get("data", {}).get("emails") or []

    def baseline_ids(self) -> set:
        return {m.get("id") for m in self._fetch("latest")}

    def latest_code(self, seen: set) -> tuple[str | None, str]:
        """返回 (code, 来源说明)。只认新邮件、且是本站的验证信。"""
        msgs = sorted(self._fetch("latest"),
                      key=lambda m: m.get("receivedAt") or "", reverse=True)
        for m in msgs:
            if m.get("id") in seen:
                continue
            sub = (m.get("subject") or "").lower()
            if "tokenpower" not in sub and "altapi" not in sub:
                continue
            code = (m.get("verificationCode") or "").strip()
            if code:
                return code, "field"
            # 回落：服务端没抽出来就自己从正文抓 6 位十六进制
            text = (m.get("textContent") or "") + " " + (m.get("subject") or "")
            hit = re.search(r"(?:code is|验证码|code)[^0-9A-Za-z]{0,12}([0-9a-fA-F]{6})\b", text)
            if hit:
                return hit.group(1), "body"
            loose = re.findall(r"\b[0-9a-fA-F]{6}\b", text)
            if loose:
                return loose[0], "body-loose"
        return None, ""


# --------------------------------------------------------------------------
# 账号与密码
# --------------------------------------------------------------------------

def normalize_uid(raw: str) -> str:
    """Matches internal/newapiconsole.NormalizeUID.

    Refuses any whitespace (a pasted value often carries a newline, and a
    newline in a header value is header injection) and anything that is not a
    positive integer. "0" and "-1" are not user ids.
    """
    if not raw:
        return ""
    if any(c in raw for c in " \t\r\n"):
        return ""
    if not raw.isdigit():
        return ""
    v = raw.lstrip("0")
    return v


def session_value(raw: str) -> str:
    """Matches internal/newapiconsole.SessionValue.

    Accepts a bare value or a whole "session=…; other=…" string pasted from
    DevTools; returns the bare value or "".
    """
    v = (raw or "").strip()
    if not v:
        return ""
    if "session=" in v:
        for part in v.split(";"):
            part = part.strip()
            if part.startswith("session="):
                return part[len("session="):]
        return ""
    if any(c in v for c in " \t\r\n"):
        return ""
    return v


def gen_password(n: int = 16) -> str:
    core = [random.choice(string.ascii_uppercase), random.choice(string.ascii_lowercase),
            random.choice(string.digits), random.choice("!@#$%^&*")]
    alphabet = string.ascii_letters + string.digits + "!@#$%^&*"
    core += [random.choice(alphabet) for _ in range(n - 4)]
    random.shuffle(core)
    return "".join(core)


def username_from_email(email: str) -> str:
    """从邮箱本地部分派生用户名，**尽量保留原形态**。

    站点要求 3–20 位，只禁掉真正非法的字符。

    为什么要保留点号：2026-11-17 对 OVH 上 109 个 altapi 账号做横向统计，
    站内用户名含点号的占 6/109、首字符为数字的占 8/109；而「去掉点号后
    截断」这一种清洗方式会产出 0/10 点号、0/10 数字开头 —— 把这批号和
    全体摆在一起，生成器的边界立刻显形。保持邮箱原貌比统一清洗更接近
    真实用户的随手填写。

    只在真的非法时才兜底，且兜底也延续原前缀而不是整个替换。
    """
    local = email.split("@")[0].lower()
    # iCloud 的 Hide-My-Email 形如 shellac-visitor.3；把 @ 之外的非法字符剔掉，
    # 但保留 . - _ 与数字前缀
    u = re.sub(r"[^a-z0-9._-]", "", local)
    u = re.sub(r"[._-]{2,}", lambda m: m.group(0)[0], u)   # 折叠重复分隔符
    u = u.strip("._-")
    if len(u) < 3:
        u = (u or "u") + "".join(random.choices(string.digits, k=6))
    return u[:20]


# --------------------------------------------------------------------------
# 注册流程
# --------------------------------------------------------------------------

class Registrar:
    def __init__(self, base: str, proxy: str | None, fp: dict,
                 settings: dict, em: Emitter):
        self.base = base
        self.http = HTTP(base, fp, proxy)
        self.fp = fp
        self.st = settings
        self.em = em
        self._last_result: dict = {}

    # -- 站点探测 --------------------------------------------------------
    def probe_status(self) -> dict:
        r = self.http.get("/api/status")
        d = (r.json() or {})
        return (d.get("data") or {})

    def check_site(self) -> None:
        st = self.probe_status()
        if not st:
            raise RuntimeError("站点 /api/status 无响应")
        if st.get("turnstile_check"):
            raise RuntimeError("站点已开启 Turnstile，需要打码，本注册机不支持")
        if not st.get("email_verification"):
            self.em.log("注意：站点已关闭邮箱验证，注册可不填验证码")
        self.em.log(f"站点={st.get('system_name')} 版本={st.get('version')} "
                    f"quota_per_unit={st.get('quota_per_unit')}")
        return st

    # -- 1. 发码 ---------------------------------------------------------
    def send_code(self, email: str) -> None:
        q = urllib.parse.urlencode({"email": email, "turnstile": ""})
        r = self.http.get(f"/api/verification?{q}", referer="/register")
        if r.cf_blocked:
            raise RuntimeError(f"发码被 CF 拦截 http={r.status}")
        d = r.json() or {}
        if not d.get("success"):
            raise RuntimeError(f"发码失败: {d.get('message') or r.text[:120]}")

    # -- 3. 注册 ---------------------------------------------------------
    def register(self, email: str, username: str, password: str,
                 code: str) -> None:
        body = {
            "username": username,
            "password": password,
            "password2": password,
            "email": email,
            "verification_code": code,
            "wechat_verification_code": "",
            "aff_code": self.st.get("aff_code") or None,
        }
        r = self.http.post("/api/user/register?turnstile=", referer="/register", body=body)
        if r.cf_blocked:
            raise RuntimeError(f"注册被 CF 拦截 http={r.status}")
        d = r.json() or {}
        if not d.get("success"):
            raise RuntimeError(f"注册失败: {d.get('message') or r.text[:160]}")

    # -- 4. 登录 ---------------------------------------------------------
    def login(self, username: str, password: str) -> dict:
        r = self.http.post("/api/user/login?turnstile=", referer="/login",
                           body={"username": username, "password": password})
        if r.cf_blocked:
            raise RuntimeError(f"登录被 CF 拦截 http={r.status}")
        d = r.json() or {}
        if not d.get("success"):
            raise RuntimeError(f"登录失败: {d.get('message') or r.text[:160]}")
        if "session" not in self.http.cookies:
            raise RuntimeError("登录成功但没拿到 session cookie")
        data = d.get("data") or {}
        # Every later /api/* call needs the real uid, not "-1".
        if data.get("id"):
            self.http.uid = normalize_uid(str(int(data["id"])))
        return data

    def self_info(self, uid: int) -> dict:
        r = self.http.get("/api/user/self",
                          referer="/console",
                          body=None)
        d = r.json() or {}
        if not d.get("success"):
            raise RuntimeError(f"/api/user/self 失败: {d.get('message') or r.text[:120]}")
        return d.get("data") or {}

    # -- 5. 建 Key -------------------------------------------------------
    def create_key(self, uid: int) -> tuple[str, str]:
        name = self.st.get("token_name") or "newapi"
        group = self.st.get("token_group") or "vip"
        body = {
            "remain_quota": 0, "expired_time": -1, "unlimited_quota": True,
            "model_limits_enabled": False, "model_limits": "",
            "cross_group_retry": False, "name": name, "group": group,
            "allow_ips": "",
        }
        r = self.http.post("/api/token/", referer="/console/token", body=body)
        d = r.json() or {}
        if not d.get("success"):
            raise RuntimeError(f"建 Key 失败: {d.get('message') or r.text[:120]}")

        r = self.http.get("/api/token/?p=1&size=100", referer="/console/token")
        items = ((r.json() or {}).get("data") or {}).get("items") or []
        cand = [t for t in items if t.get("name") == name and t.get("status") == 1]
        if not cand:
            raise RuntimeError("token 列表里找不到新建的 Key")
        tok = max(cand, key=lambda t: t.get("created_time", 0))
        tid = str(tok.get("id"))

        # 必须 POST：GET 会被网关判成 Invalid URL
        r = self.http.post(f"/api/token/{tid}/key", referer="/console/token", body={})
        d = r.json() or {}
        key = ((d.get("data") or {}).get("key") or "").strip()
        if not d.get("success") or len(key) < 20:
            raise RuntimeError(f"取 Key 失败: {d.get('message')} key={key[:12]!r}")
        return key, tid

    # -- 编排 ------------------------------------------------------------
    def run(self, email: str, mailbox_url: str, password: str | None = None,
            username: str | None = None) -> dict:
        t0 = time.time()
        password_given = bool(password)
        password = password or gen_password(16)
        username = username or username_from_email(email)

        site = self.check_site()
        unit = float(site.get("quota_per_unit") or 500000)

        # An account that already exists needs no code: logging in with the
        # stored password reaches the same end state and saves a mail.
        if password_given:
            try:
                if self._try_login_existing(email, username, password, unit, t0):
                    return self._last_result
            except RuntimeError:
                pass  # fall through to the fresh-registration path

        box = Mailbox(mailbox_url)
        seen = box.baseline_ids()

        # 发码 + 等信（同邮箱 30s 限流，重试时由调用方控制间隔）
        # 进注册页、填邮箱、点「发送验证码」——用户先看到 /register 页面才发码。
        self.check_site()
        self.send_code(email)
        self.em.update(status="sent_code", detail="已发验证码，等待邮件")
        self.em.log(f"{email} 已发码")

        code = None
        source = ""
        deadline = time.time() + float(self.st.get("verify_timeout_sec") or DEFAULT_VERIFY_TIMEOUT)
        while time.time() < deadline:
            code, source = box.latest_code(seen)
            if code:
                break
            poll_sleep()
        if not code:
            raise RuntimeError(f"mailbox_timeout: {int(deadline - time.time() + 0)}s 内没收到验证码")
        self.em.log(f"收到验证码 {code}（来源={source}）")

        # 收到信 → 切到邮箱 → 找信 → 复制验证码 → 切回页面 → 粘贴 → 提交。
        # 真人这一步十几秒到一分多钟，且长度很分散；这里用 PACE_AFTER_CODE
        # 的宽区间，并对一部分账号再追加一段长尾，避免所有账号都落在同一窄带。
        wait = pace_after_code()
        if random.random() < 0.25:
            # 25% 的账号模拟"先干别的再回来"，额外多等 20~45 秒
            wait += random.uniform(20.0, 45.0)
        self.em.log(f"收到验证码，停顿 {wait:.0f}s 后填写（模拟人工操作）")
        self.em.update(detail=f"收到验证码，{wait:.0f}s 后提交")

        try:
            self.register(email, username, password, code)
            self.em.update(status="registered", username=username, password=password,
                           detail="注册成功，正在登录")
            self.em.log("注册成功")
        except RuntimeError as e:
            # A batch that died between POST /register and login leaves a real
            # account behind; rerunning would answer "邮箱地址已被占用".
            # Recover by logging in with the password this attempt would have
            # used — but only when the caller supplied one, since a freshly
            # generated password cannot be the one the account was made with.
            msg = str(e)
            if "已被占用" not in msg and "already" not in msg.lower():
                raise
            if not password_given:
                raise RuntimeError(
                    "该邮箱已注册过，且本次没带原密码，无法接管；"
                    "请把候选里已有的密码填上后重试") from e
            self.em.log(f"{email} 已存在账号，改用登录接管（不重复注册）")
            self.em.update(status="registered", username=username, detail="账号已存在，转为登录接管")

        return self.finish(email, username, password, unit, t0)

    # -- 已存在账号的接管：直接登录，不发码不注册 ------------------------
    def _try_login_existing(self, email: str, username: str, password: str,
                            unit: float, t0: float) -> bool:
        """Returns True when the account already existed and was finished."""
        self.check_site()
        pace(PACE_STEP)
        r = self.http.post("/api/user/login?turnstile=", referer="/login",
                           body={"username": username, "password": password})
        d = (r.json() or {}) if r.text.strip() else {}
        if not d.get("success"):
            # Wrong password or no such account: caller falls back to a fresh
            # registration (which will report "已被占用" for a real duplicate).
            raise RuntimeError(d.get("message") or "login probe failed")
        self.em.log(f"{email} 账号已存在，直接登录接管")
        self.em.update(status="verified", username=username, detail="已登录，正在生成 API Key")
        self._last_result = self.finish(email, username, password, unit, t0)
        return True

    # -- 收尾：登录 -> 读身份 -> 建 Key ---------------------------------
    def finish(self, email: str, username: str, password: str,
               unit: float, t0: float) -> dict:
        # 注册成功到登录：用户会先看结果页/或等跳转，不会立刻发登录请求。
        pace(PACE_AFTER_REGISTER)
        info = self.login(username, password)
        uid = int(info.get("id") or 0)
        # 登录后控制台要先渲染，才会读到用户信息。
        pace(PACE_AFTER_LOGIN)
        me = self.self_info(uid) if uid else {}
        session = self.http.cookies.get("session", "")
        self.em.update(status="verified", account_id=str(uid), session_cookie=session,
                       group=me.get("group") or info.get("group"),
                       quota=me.get("quota"), used_quota=me.get("used_quota"),
                       quota_per_unit=unit,
                       balance_usd=round(float(me.get("quota") or 0) / unit, 4),
                       detail="已登录，正在生成 API Key")
        self.em.log(f"登录成功 uid={uid} group={me.get('group')} "
                    f"余额=${float(me.get('quota') or 0)/unit:.2f}")

        # 读身份 → 进 /console/token → 填表 → 建 Key
        pace(PACE_STEP)
        key, tid = self.create_key(uid)
        self.em.log(f"已生成 API Key id={tid}")
        self.em.done("key_created", api_key=key, token_id=tid, account_id=str(uid),
                     session_cookie=session, username=username, password=password,
                     group=me.get("group") or "",
                     quota=me.get("quota"), used_quota=me.get("used_quota"),
                     quota_per_unit=unit,
                     balance_usd=round(float(me.get("quota") or 0) / unit, 4),
                     elapsed=round(time.time() - t0, 1))
        return {"api_key": key, "token_id": tid, "account_id": str(uid),
                "session_cookie": session, "username": username,
                "password": password, "email": email}


# --------------------------------------------------------------------------
# CLI
# --------------------------------------------------------------------------

def run_job(path: str) -> int:
    """--job-file: {"settings":{...},"items":[{"index":0,"email":..,"mailbox_url":..}]}"""
    with open(path, encoding="utf-8") as f:
        job = json.load(f)
    st = job.get("settings") or {}
    items = job.get("items") or []
    base = st.get("base_url") or DEFAULT_BASE
    gap = float(st.get("min_interval_sec") or DEFAULT_SEND_GAP)
    proxies = [p for p in (st.get("proxy_pool") or []) if p.strip()]
    set_pace_scale(st.get("pace_scale", 1.0))
    set_code_delay(st.get("code_delay_sec", 0))

    rc = 0
    # 批次稀释区间：由设置项 batch_gap_sec 给出中心值，区间取 [0.7x, 1.5x]
    batch_gap = float(st.get("batch_gap_sec") or 0)
    last_send = 0.0
    first = True
    for it in items:
        idx = int(it.get("index") or 0)
        email = it.get("email") or ""
        em = Emitter(idx, email)
        proxy = it.get("proxy_url") or (proxies[idx % len(proxies)] if proxies else None)
        fp = FP_PROFILES[idx % len(FP_PROFILES)]
        # 批次稀释：账号之间拉开距离，避免 uid 连成一段
        if not first and batch_gap > 0:
            lo, hi = max(1.0, batch_gap * 0.7), max(2.0, batch_gap * 1.5)
            d = random.uniform(lo, hi)
            em.log(f"批次稀释：等待 {d:.0f}s 再注册下一个（避免 uid 连号）")
            time.sleep(d)
        first = False
        # 同邮箱发码间隔：批次内也守住 30s
        wait = gap - (time.time() - last_send)
        if last_send and wait > 0:
            em.log(f"等待 {wait:.0f}s 满足发码间隔")
            time.sleep(wait)
        try:
            em.update(status="running", detail="开始注册", proxy_url=proxy or "")
            reg = Registrar(base, proxy, fp, st, em)
            last_send = time.time()
            reg.run(email, it.get("mailbox_url") or "",
                    it.get("password"), it.get("username"))
        except Exception as e:  # noqa: BLE001
            em.done("failed", error=str(e)[:300])
            rc = 1
    return rc


def main() -> int:
    ap = argparse.ArgumentParser(description="altapi.ai 注册机")
    ap.add_argument("--job-file", help="批处理任务 JSON")
    ap.add_argument("--email")
    ap.add_argument("--mailbox")
    ap.add_argument("--proxy")
    ap.add_argument("--base", default=os.environ.get("ALTAPI_BASE") or DEFAULT_BASE)
    ap.add_argument("--password")
    ap.add_argument("--username")
    ap.add_argument("--token-group", default="vip")
    ap.add_argument("--probe", action="store_true", help="只探测站点状态")
    ap.add_argument("--probe-proxy", action="store_true", help="探测代理出口 IP")
    args = ap.parse_args()

    if args.probe_proxy:
        tp = Transport(args.proxy, impersonate="chrome")
        status, text, _ = tp.request("GET", "https://api.ipify.org?format=json",
                                     {"Accept": "application/json"}, None, 20)
        print(json.dumps({"ok": status == 200, "transport": tp.mode,
                          "body": text[:200]}, ensure_ascii=False))
        return 0 if status == 200 else 1

    if args.probe:
        h = HTTP(args.base, FP_PROFILES[0], args.proxy)
        r = h.get("/api/status")
        d = (r.json() or {}).get("data") or {}
        print(json.dumps({
            "ok": bool(d),
            "system_name": d.get("system_name"),
            "version": d.get("version"),
            "turnstile_check": d.get("turnstile_check"),
            "email_verification": d.get("email_verification"),
            "quota_per_unit": d.get("quota_per_unit"),
            "status": r.status,
        }, ensure_ascii=False, indent=2))
        return 0 if d else 1

    if args.job_file:
        return run_job(args.job_file)

    if not (args.email and args.mailbox):
        ap.error("需要 --job-file，或 --email + --mailbox")

    em = Emitter(0, args.email)
    st = {"verify_timeout_sec": DEFAULT_VERIFY_TIMEOUT, "token_group": args.token_group,
          "token_name": "newapi"}
    try:
        reg = Registrar(args.base, args.proxy, FP_PROFILES[0], st, em)
        reg.run(args.email, args.mailbox, args.password, args.username)
        return 0
    except Exception as e:  # noqa: BLE001
        em.done("failed", error=str(e)[:300])
        return 1


if __name__ == "__main__":
    sys.exit(main())
