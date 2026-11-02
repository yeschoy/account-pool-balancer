from __future__ import annotations

import base64
import hashlib
import json
import os
from datetime import datetime, timezone
from urllib.parse import parse_qs, quote, unquote, urlencode, urljoin, urlsplit

from curl_cffi import requests as curl_requests
from curl_cffi.const import CurlOpt
from logicc_protocol import Pacer, ProxyConfig
from open_design_github import GITHUB, GITHUB_HOSTS, GithubAccount, GithubBrowser, MailboxReader
from river_chain_proxy import ChainRoute, LocalChainProxy
# 登录入口：用户给的就是这个地址（next=%2Fperchai）。
SIGN_IN = "https://chat.perchai.app/sign-in?next=%2Fperchai"
# 平台自己的主机（Perch 聊天界面）。
CHAT_HOST = "chat.perchai.app"
# app.perchai.app 是 Perch Terminal（静态站），同一账号也应该能进去。
TERMINAL_HOST = "app.perchai.app"
HOSTS = (CHAT_HOST, TERMINAL_HOST)

# Supabase：前端 chunk 里写死的项目地址；可通过 .env 覆盖（站点换 key/项目时不改代码）。
SUPABASE_URL = (os.environ.get("PERCH_SUPABASE_URL") or
                "https://zlfuvsfjtgsdtqcaykia.supabase.co").rstrip("/")
SUPABASE_REF = urlsplit(SUPABASE_URL).hostname.split(".")[0] if urlsplit(SUPABASE_URL).hostname else "supabase"
AUTH_URL = f"{SUPABASE_URL}/auth/v1"
# 前端的公开客户端密钥（打包进每个访客浏览器的 publishable key，不是服务端密钥）。
# Supabase 的 /auth/v1/* 要求带 `apikey`，缺了就是 401「No API key found in request」。
SUPABASE_KEY = (os.environ.get("PERCH_SUPABASE_KEY") or
                "sb_publishable_w4R_pNkUpygBFIljZCoOlA_67ACg0FO")

# 浏览器窗口默认打开的页面；Perch Terminal 与聊天界面在同一个 Supabase 会话下。
SITE_URL = f"https://{CHAT_HOST}/perchai"
TERMINAL_URL = f"https://{TERMINAL_HOST}/perchai"

SESSION_ENDPOINT = f"https://{CHAT_HOST}/api/auth/session"
PROBE_ENDPOINT = f"https://{CHAT_HOST}/auth/callback"     # 不带 code：会话有效时不会跳 sign-in

# @supabase/ssr 的会话 cookie 规则（chunked / base64-）。
COOKIE_BASE = f"sb-{SUPABASE_REF}-auth-token"
VERIFIER_COOKIE = f"{COOKIE_BASE}-code-verifier"
CHUNK_SIZE = 3180
BASE64_PREFIX = "base64-"

# 属于 Perch 自己/它的认证商才允许注入；其它 Cookie 一律丢弃。
DOMAINS = {CHAT_HOST, TERMINAL_HOST, "perchai.app", "supabase.co"}
# 会话 cookie 所在域：Domain=.perchai.app（README 里说明的「两个域名共用」）。
PERCHAI_SUFFIX = "perchai.app"
SESSION_DOMAIN = f".{PERCHAI_SUFFIX}"
class PerchConsoleError(RuntimeError):
    """Perch 侧无法自动完成；消息可直接展示给用户。"""
def now() -> str:
    return datetime.now(timezone.utc).isoformat()


# ----------------------------------------------------------------- 编码工具


def code_verifier() -> str:
    """RFC 7636 verifier：Supabase 用 56 字节 → base64url 得 75 字符，落在 43–128 内。"""
    return base64.urlsafe_b64encode(os.urandom(56)).decode().rstrip("=")


def code_challenge(verifier: str) -> str:
    digest = hashlib.sha256(verifier.encode()).digest()
    return base64.urlsafe_b64encode(digest).decode().rstrip("=")


def base64url_json(payload: object) -> str:
    raw = json.dumps(payload, separators=(",", ":"), ensure_ascii=False).encode()
    return base64.urlsafe_b64encode(raw).decode().rstrip("=")


def chunk_cookie(name: str, value: str) -> list[tuple[str, str]]:
    """按 @supabase/ssr 的规则切分超长 cookie：`name.0`、`name.1`……"""
    encoded = quote(value, safe="")
    if len(encoded) <= CHUNK_SIZE:
        return [(name, value)]
    parts, rest = [], encoded
    while rest:
        piece = rest[:CHUNK_SIZE]
        cut = piece.rfind("%")          # 不能把 %XX 转义切断
        if cut > CHUNK_SIZE - 3:
            piece = piece[:cut]
        parts.append(unquote(piece))
        rest = rest[len(piece):]
    return [(f"{name}.{index}", part) for index, part in enumerate(parts)]
def session_cookie_value(session: dict) -> str:
    """把 Supabase 会话对象编码成 cookie 值（@supabase/ssr 的 base64url 编码）。"""
    return BASE64_PREFIX + base64url_json(session)
def session_cookies(session: dict, expires_at: int | None) -> list[dict]:
    """生成两个主机的会话 cookie。Domain=.perchai.app 让 chat 与 app 共用一份会话；
    分片形式以 host-only 写入（分片 cookie 带 Domain 时，浏览器里的合并行为不可靠）。"""
    chunks = chunk_cookie(COOKIE_BASE, session_cookie_value(session))
    result: list[dict] = []
    for name, value in chunks:
        result.append({"name": name, "value": value, "domain": SESSION_DOMAIN,
                       "path": "/", "secure": True, "httpOnly": False, "hostOnly": False,
                       "expires": expires_at})
    return result
class RouteSession:
    """HTTP session routed through the explicitly supplied proxy only."""

    def __init__(self, proxy: ProxyConfig | None):
        self.chain = LocalChainProxy(ChainRoute(system=None, upstream=proxy.url)) if proxy else None

    @property
    def session_proxy(self):
        return self.chain.url if self.chain else None

    def close(self):
        if self.chain:
            self.chain.close()

    def client(self):
        proxy = self.session_proxy
        return curl_requests.Session(
            impersonate="chrome", proxy=proxy, trust_env=False,
            curl_options={CurlOpt.PROXY: proxy or "", CurlOpt.NOPROXY: ""},
            headers={"Accept-Language": "en-US,en;q=0.9"})


def supabase_authorize_url(redirect_to: str, verifier: str, *, provider: str = "github") -> str:
    """Supabase `/auth/v1/authorize`：返回 302 到 GitHub 的授权页。"""
    query = urlencode({"provider": provider, "redirect_to": redirect_to,
                       "code_challenge": code_challenge(verifier),
                       "code_challenge_method": "s256"})
    return f"{AUTH_URL}/authorize?{query}"


def callback_redirect(host: str = CHAT_HOST, *, next_path: str = "/perchai") -> str:
    """`redirect_to` 必须在 Supabase 白名单里：站点自己的 /auth/callback（带 next）。"""
    return f"https://{host}/auth/callback?next={quote(next_path, safe='')}"


def is_callback(url: str) -> bool:
    """授权成功后 GitHub 的第一跳是 Supabase 自己的回调（带 code），不是 Perch 的。

    GitHub → `https://<ref>.supabase.co/auth/v1/callback?code=...&iss=...`
           → `https://chat.perchai.app/auth/callback?code=...&next=%2Fperchai`
    所以这里以 Supabase 回调为终点，再由 `follow_callback` 顺着后续跳转拿到 Perch 的 code。
    """
    parts = urlsplit(url)
    if parts.hostname == urlsplit(SUPABASE_URL).hostname and parts.path == "/auth/v1/callback":
        return True
    return parts.hostname == CHAT_HOST and parts.path == "/auth/callback"


def callback_host(host: str) -> bool:
    """回调链路上允许的主机：Supabase 项目域 + Perch 自己的两个前端域。

    `app.perchai.app` 是 Perch Terminal，它会 307 到 `chat.perchai.app/auth/callback`，
    同样的 code 再落一次，所以这一跳必须放行。
    """
    host = (host or "").lower()
    return host == urlsplit(SUPABASE_URL).hostname or host in {CHAT_HOST, TERMINAL_HOST}


def supabase_code(url: str) -> str:
    """Supabase 回调里的 GitHub 授权码（此时还不是 Supabase 的 PKCE 会话码）。"""
    return (parse_qs(urlsplit(url).query).get("code") or [""])[0]


def follow_callback(client, landing: str) -> str:
    """顺着 Supabase 回调的后续跳转，拿到 Perch 的 `/auth/callback?code=...`。

    Supabase 回调会把一次性的 code 换成授权结果并 302 到站点的 `redirect_to`；
    每一跳都必须留在 Supabase / Perch 域内，绝不跟到第三方。
    """
    current, seen = landing, set()
    for _ in range(6):
        if urlsplit(current).hostname == CHAT_HOST and urlsplit(current).path == "/auth/callback":
            return current
        if current in seen:
            break
        seen.add(current)
        page = client.get(current, allow_redirects=False, timeout=45,
                          headers={"apikey": SUPABASE_KEY})
        location = page.headers.get("location", "")
        if page.status_code not in (301, 302, 303, 307, 308) or not location:
            break
        current = urljoin(current, location)
        if not callback_host(urlsplit(current).hostname or ""):
            raise PerchConsoleError(f"授权回调跳转到站外地址：{current[:100]}")
    raise PerchConsoleError("授权回调没有回到 Perch 的 /auth/callback")


def github_authorize_url(location: str) -> str:
    """从 Supabase 的 302 里取出 GitHub 授权地址并校验；不合法直接失败，绝不盲目跟随。"""
    target = urlsplit(location)
    if target.scheme != "https" or target.hostname != "github.com" or \
            target.path != "/login/oauth/authorize":
        raise PerchConsoleError("Supabase 未返回 GitHub 授权地址，页面可能已变化")
    return location


def authorize(browser: GithubBrowser, url: str, account: GithubAccount) -> str:
    """在 GitHub 侧完成登录并确认授权，返回 Supabase 的回调地址（含 code）。"""
    for attempt in range(3):
        page = browser.session.request("GET", url, allow_redirects=False, timeout=45)
        location = page.headers.get("location", "")
        if page.status_code in (301, 302, 303, 307, 308):
            landing = urljoin(GITHUB, location)
            if is_callback(landing):
                return landing
            path = urlsplit(landing).path
            if urlsplit(landing).hostname in GITHUB_HOSTS and "/login" in path:
                browser.sign_in(account)       # 未登录：登录后回到授权页
                continue
            raise PerchConsoleError(f"GitHub 授权跳转到未知地址：{location[:100]}")
        if page.status_code != 200:
            raise PerchConsoleError(f"GitHub 授权页返回 HTTP {page.status_code}")
        try:
            fields = authorize_fields(page.text)
            return submit_authorize(browser, url, fields)
        except _RetryAuthorize as exc:
            if attempt < 2:
                browser.pacer.pause("GitHub 授权页返回异常，重试前")
                continue
            raise PerchConsoleError(str(exc)) from None
    raise PerchConsoleError("GitHub 授权未完成")


class _RetryAuthorize(PerchConsoleError):
    """可重试的授权异常：中间页或表单异常。"""


def authorize_fields(html: str) -> dict:
    """解析授权页表单，返回可提交的字段。

    三种异常分得很清楚，别让调用方把「不可重试」当成「可重试」：
    * 明确的拒绝提示 → `PerchConsoleError`（不重试）；
    * 回调地址不是 Supabase 的 `/auth/v1/callback` → `PerchConsoleError`（绝不提交，防止把 code 送给别的主机）；
    * 普通异常页 / 缺字段 → `_RetryAuthorize`（重进授权页再试一次）。
    """
    fields = GithubBrowser._inputs(html)
    if "authorize" not in fields:
        # 没有授权按钮时才看 flash：只有真正的拒绝页才不可重试。
        flash = GithubBrowser._flash(html)
        if flash:
            raise PerchConsoleError(f"GitHub 拒绝了授权：{flash}")
        raise _RetryAuthorize(f"GitHub 授权页异常：{GithubBrowser._text(html)[:120]}")
    missing = {"authenticity_token", "client_id", "redirect_uri", "state"} - set(fields)
    if missing:
        raise _RetryAuthorize(f"GitHub 授权表单缺少字段：{sorted(missing)}")
    target = urlsplit(fields["redirect_uri"])
    if target.hostname != urlsplit(SUPABASE_URL).hostname or target.path != "/auth/v1/callback":
        raise PerchConsoleError("GitHub 授权回调地址与 Supabase 预期不一致")
    return fields


def submit_authorize(browser: GithubBrowser, authorize_url: str, fields: dict) -> str:
    """提交「Authorize」；成功后 GitHub 会 302 回 Supabase 回调（带 code）。

    上游回的是 200 时，只有拿到明确的拒绝提示才判定失败，其余按可重试处理。
    """
    data = dict(fields)
    data["authorize"] = "1"
    browser.pacer.pause("确认 GitHub 授权前")
    response = browser.session.request(
        "POST", f"{GITHUB}/login/oauth/authorize", data=data, allow_redirects=False,
        timeout=45, headers={"Referer": authorize_url, "Origin": GITHUB})
    if response.status_code in (301, 302, 303, 307, 308):
        location = response.headers.get("location", "")
        landing = urljoin(f"{GITHUB}/", location)
        if is_callback(landing):
            return landing
        raise PerchConsoleError(f"授权后未回到 Perch 回调：{location[:100]}")
    # 返回 200 时仍可能是「已授权但没有表单」的中间页，重进授权页再试；
    # 只有在明确的拒绝提示出现时才判定失败。
    flash = GithubBrowser._flash(response.text)
    if flash:
        raise PerchConsoleError(f"GitHub 拒绝了授权：{flash}")
    raise _RetryAuthorize(f"GitHub 授权提交返回 HTTP {response.status_code}（页面异常）")


def parse_redirected_callback(url: str) -> tuple[str, str]:
    """从 `.../auth/callback?code=...&next=...` 里取 (code, next)，并校验主机。"""
    parts = urlsplit(url)
    if parts.hostname != CHAT_HOST or parts.path != "/auth/callback":
        raise PerchConsoleError("Supabase 回调地址不是 Perch 的 /auth/callback")
    query = parse_qs(parts.query)
    code = (query.get("code") or [""])[0]
    if not code:
        error = (query.get("error_description") or query.get("error") or [""])[0]
        raise PerchConsoleError(f"Supabase 回调未带 code{('：' + error) if error else ''}")
    return code, (query.get("next") or ["/perchai"])[0]


def body_hint(text: str, limit: int = 160) -> str:
    """从错误响应里取出可读原因（Supabase 会回 {"error_description": ...}），不含任何凭据。"""
    try:
        payload = json.loads(text or "{}")
    except ValueError:
        return str(text or "")[:limit]
    if isinstance(payload, dict):
        for key in ("error_description", "msg", "message", "error"):
            value = payload.get(key)
            if isinstance(value, str) and value.strip():
                return value.strip()[:limit]
    return str(text or "")[:limit]


def exchange(client, auth_code: str, verifier: str) -> dict:
    """`grant_type=pkce` 换会话。返回 Supabase 的会话对象（含 access/refresh token）。"""
    response = client.post(f"{AUTH_URL}/token?grant_type=pkce",
                           json={"auth_code": auth_code, "code_verifier": verifier},
                           headers={"Content-Type": "application/json",
                                    "apikey": SUPABASE_KEY,
                                    "X-Client-Info": "supabase-ssr/0.10.3"},
                           timeout=45)
    if response.status_code >= 400:
        raise PerchConsoleError(
            f"Supabase 用 code 换会话失败（HTTP {response.status_code}）：{body_hint(response.text)}")
    try:
        payload = response.json()
    except ValueError:
        raise PerchConsoleError("Supabase 返回的会话不是 JSON") from None
    if not isinstance(payload, dict) or not isinstance(payload.get("access_token"), str) \
            or not payload["access_token"]:
        raise PerchConsoleError("Supabase 未返回 access_token")
    return payload


def identity(session: dict) -> tuple[str, str]:
    """从会话里取 (邮箱, user id)。"""
    user = session.get("user") if isinstance(session.get("user"), dict) else {}
    email = user.get("email") if isinstance(user.get("email"), str) else ""
    return email, str(user.get("id") or "")


def sign_in(account: GithubAccount, *, proxy: ProxyConfig | None, pacer: Pacer,
            log=print) -> dict:
    """一次性登录取回两个主机的会话 cookie；返回保存用的记录片段。"""
    verifier = code_verifier()
    route = RouteSession(proxy)
    client = mailbox = browser = None
    started = now()
    try:
        client = route.client()
        pacer.pause("创建 Perch 授权请求前")
        target = callback_redirect(CHAT_HOST)
        start = client.get(supabase_authorize_url(target, verifier), allow_redirects=False,
                           timeout=45, headers={"Accept": "application/json",
                                                "apikey": SUPABASE_KEY})
        if start.status_code not in (301, 302, 303, 307, 308):
            raise PerchConsoleError(f"Supabase 授权入口返回 HTTP {start.status_code}")
        authorize_url = github_authorize_url(urljoin(SUPABASE_URL, start.headers.get("location", "")))
        mailbox = MailboxReader(account.mailbox_url, pacer, proxy=proxy)
        browser = GithubBrowser(pacer, None, session=client, mailbox=mailbox,
                                totp_secret=account.totp_secret)
        landing = authorize(browser, authorize_url, account)
        # GitHub → Supabase 回调 → Perch 回调；Supabase 那一跳的 code 只是 GitHub 的授权码，
        # 不能拿去换会话，必须拿到 Perch 回调里 Supabase 签发的那一个。
        if urlsplit(landing).hostname != CHAT_HOST:
            landing = follow_callback(client, landing)
        auth_code, _next = parse_redirected_callback(landing)
        pacer.pause("用 code 换 Supabase 会话前")
        session = exchange(client, auth_code, verifier)
        email, user_id = identity(session)
        expires_in = session.get("expires_in")
        expires_at = int(datetime.now(timezone.utc).timestamp() + expires_in) \
            if isinstance(expires_in, (int, float)) and expires_in > 0 else None
        cookies = session_cookies(session, expires_at)
        log(f"Perch 登录会话已建立{('（' + email + '）') if email else ''}")
        return {"cookies": cookies, "email": email, "user_id": user_id,
                "supabase_session": session, "logged_in_at": now(),
                "expires_at": expires_at, "login_started_at": started}
    finally:
        if browser is not None:
            browser.close()        # 与 client 同一个对象，只关一次
        elif client is not None:
            client.close()
        if mailbox is not None:
            mailbox.close()
        route.close()
