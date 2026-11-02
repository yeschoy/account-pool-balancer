from __future__ import annotations

import hashlib
import hmac
import json
import os
import re
import secrets
import zipfile
from datetime import datetime, timezone
from pathlib import Path
from typing import Any
SCHEMA_VERSION = "1.0"
PROVIDER = "perchai"
AUTH_PROVIDER = "github"

# 前端公开的 publishable key（与 perch_console.SUPABASE_KEY 同源）。
# 导出时要写进 auth_context，导入方才能发 Supabase 请求。
PERCH_SUPABASE_KEY = (os.environ.get("PERCH_SUPABASE_KEY") or
                      "sb_publishable_w4R_pNkUpygBFIljZCoOlA_67ACg0FO")
# Access Token 提前 60 秒视为过期，避免刚好卡在边界上。
EXPIRY_SKEW = 60
# 日志里最多显示 Token 前后各 4 个字符；更短的直接打码。
TOKEN_MARK = 12

# 文件名里不允许出现的字符（路径穿越与各平台保留字符）。
_UNSAFE = re.compile(r"[^A-Za-z0-9@._+-]")


# 代理隧道的瞬时故障：换一条新连接通常就好，值得重试一次。
TRANSIENT = ("SSLError", "ProxyError", "ConnectionError", "Timeout", "TimeoutError",
             "CurlError", "TLS", "Connection reset", "timed out",
             # curl 的隧道类错误码：56 = 收数据时连接中断，35 = TLS 握手失败，52/55/28 = 空响应/发送失败/超时
             "curl: (35)", "curl: (52)", "curl: (55)", "curl: (56)", "curl: (28)",
             "invalid library", "OPENSSL_internal", "Proxy CONNECT aborted")


def transient(exc: BaseException) -> bool:
    """是不是「重试可能成功」的网络故障（而不是账号/凭证问题）。"""
    text = f"{type(exc).__name__} {exc}"
    return any(marker.lower() in text.lower() for marker in TRANSIENT)
def body_hint(text: str, limit: int = 160) -> str:
    """从错误响应里取出可读原因（Supabase 回 {"error_description"/"msg"/"error"}），不含凭据。"""
    try:
        payload = json.loads(text or "{}")
    except ValueError:
        return str(text or "")[:limit]
    if isinstance(payload, dict):
        for key in ("error_description", "msg", "message", "error", "error_code"):
            value = payload.get(key)
            if isinstance(value, str) and value.strip():
                return value.strip()[:limit]
    return str(text or "")[:limit]


class SessionError(RuntimeError):
    """会话不完整 / 未通过验证；消息可直接展示且不含 Token。"""
def now() -> str:
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def unix_now() -> int:
    return int(datetime.now(timezone.utc).timestamp())
class SessionExtractor:
    """从账号记录里抽出标准 Session（不发起任何网络请求）。"""

    @staticmethod
    def extract(account: dict) -> dict:
        """返回规范化后的会话对象；缺字段直接抛 SessionError，不做猜测。"""
        if not isinstance(account, dict):
            raise SessionError("账号记录无效")
        session = account.get("supabase_session")
        if not isinstance(session, dict):
            raise SessionError("该账号还没有 Supabase 会话，请先完成 GitHub 授权登录")
        access = session.get("access_token")
        refresh = session.get("refresh_token")
        if not isinstance(access, str) or not access:
            raise SessionError("会话缺少 access_token")
        if not isinstance(refresh, str) or not refresh:
            raise SessionError("会话缺少 refresh_token")
        user = session.get("user") if isinstance(session.get("user"), dict) else {}
        user_id = user.get("id")
        if not isinstance(user_id, str) or not user_id:
            raise SessionError("会话缺少 Supabase 用户 ID")
        email = user.get("email")
        if not isinstance(email, str) or "@" not in email:
            meta = user.get("user_metadata") if isinstance(user.get("user_metadata"), dict) else {}
            email = meta.get("email") if isinstance(meta.get("email"), str) else ""
        if not email:
            raise SessionError("会话缺少邮箱，无法命名导出文件")
        expires_at = session.get("expires_at")
        if not isinstance(expires_at, (int, float)) or expires_at <= 0:
            # Supabase 老版本只给 expires_in；用登录时间换算，仍然保持 Unix 秒。
            issued = session.get("expires_in")
            if isinstance(issued, (int, float)) and issued > 0:
                expires_at = unix_now() + int(issued)
            else:
                raise SessionError("会话缺少 expires_at")
        provider = (user.get("app_metadata") or {}).get("provider") \
            if isinstance(user.get("app_metadata"), dict) else None
        github_id = SessionExtractor.github_id(user)
        created = user.get("created_at")
        return {
            "access_token": access,
            "refresh_token": refresh,
            "expires_at": int(expires_at),
            "supabase_user_id": user_id,
            "email": email,
            "auth_provider": provider if provider == AUTH_PROVIDER else AUTH_PROVIDER,
            "github_user_id": github_id,
            "registered_at": created if isinstance(created, str) and created else
            (account.get("logged_in_at") or now()),
            "login": account.get("login") or "",
        }

    @staticmethod
    def github_id(user: dict) -> str | None:
        """GitHub 数字 ID 只从 identities 里取（Supabase 会把它原样放进去）。"""
        for identity in user.get("identities") or []:
            if not isinstance(identity, dict):
                continue
            if identity.get("provider") != AUTH_PROVIDER:
                continue
            value = identity.get("id")
            if isinstance(value, str) and value:
                return value
        meta = user.get("user_metadata") if isinstance(user.get("user_metadata"), dict) else {}
        for key in ("provider_id", "sub"):
            value = meta.get(key)
            if isinstance(value, str) and value.isdigit():
                return value
        return None

    @staticmethod
    def to_document(session: dict, *, supabase_url: str, workspace_id: str | None = None,
                    supabase_anon_key: str | None = None) -> dict:
        """拼成标准导出 JSON；缺的可选字段一律 null，不填假值。

        `auth_context.supabase_anon_key` 是**必需**的：导入方要用它发 Supabase 请求。
        它是站点打包下发到每个访客浏览器的公开客户端密钥（publishable key），
        不是服务端密钥，所以可以随导出一起给。
        """
        if supabase_anon_key is None:
            # 由调用方传入；这里不猜，缺了就让导入方明确知道。
            supabase_anon_key = PERCH_SUPABASE_KEY or None
        return {
            "schema_version": SCHEMA_VERSION,
            "provider": PROVIDER,
            "auth_provider": AUTH_PROVIDER,
            "account": {
                "supabase_user_id": session["supabase_user_id"],
                "email": session["email"],
                "github_user_id": session.get("github_user_id"),
                "registered_at": session.get("registered_at") or now(),
            },
            "session": {
                "access_token": session["access_token"],
                "refresh_token": session["refresh_token"],
                "expires_at": int(session["expires_at"]),
            },
            "auth_context": {
                "supabase_url": supabase_url,
                "workspace_id": workspace_id,
                "supabase_anon_key": supabase_anon_key,
            },
        }
class SessionValidator:
    """导出前验证会话：身份、归属、有效期、必要的首次登录初始化。

    `client_factory()` 需要返回一个已经挂好**该账号代理**的 HTTP 会话；
    验证请求只发往 Supabase 自己的 `/auth/v1/*`，不会消耗任何模型额度。
    """

    def __init__(self, *, supabase_url: str, supabase_key: str, client_factory):
        self.supabase_url = supabase_url.rstrip("/")
        self.supabase_key = supabase_key
        self.client_factory = client_factory

    # -- 内部请求 -------------------------------------------------------
    def _client(self):
        return self.client_factory()

    def refresh(self, session: dict, client=None) -> dict:
        """用 refresh_token 换新会话；Supabase 会轮换 refresh_token，必须整份写回。"""
        owned = client is None
        client = client or self._client()
        try:
            response = client.post(
                f"{self.supabase_url}/auth/v1/token?grant_type=refresh_token",
                json={"refresh_token": session["refresh_token"]},
                headers={"Content-Type": "application/json", "apikey": self.supabase_key},
                timeout=45)
        finally:
            if owned:
                client.close()
        if response.status_code >= 400:
            # 三种情况必须分开说，否则 429 会被当成「凭证坏了」逼用户重新登录：
            # 429 = 请求太频繁（等一会儿再试，凭证是好的）；
            # refresh_token_already_used / invalid_grant = 真的失效了，需要重新授权；
            # 其余 = 上游故障，可重试。
            detail = body_hint(response.text)
            if response.status_code == 429:
                raise SessionError(
                    "刷新会话被限流（HTTP 429）：凭证仍然有效，请稍后再试，不必重新登录")
            if re.search(r"already_used|invalid_grant|refresh_token_not_found|not valid",
                         detail, re.IGNORECASE):
                raise SessionError(
                    "刷新会话失败：refresh_token 已失效（可能已被别处使用），请重新授权登录")
            raise SessionError(f"刷新会话失败（HTTP {response.status_code}）：{detail[:120]}")
        try:
            payload = response.json()
        except ValueError:
            raise SessionError("刷新会话失败：返回内容不是 JSON") from None
        if not isinstance(payload, dict) or not payload.get("access_token"):
            raise SessionError("刷新会话失败：没有拿到新的 access_token")
        if not payload.get("refresh_token"):
            payload["refresh_token"] = session["refresh_token"]
        return payload

    def whoami(self, access_token: str, client=None) -> dict:
        """可信身份来源：Supabase 用这个 Token 回读它自己的用户记录。"""
        owned = client is None
        client = client or self._client()
        try:
            response = client.get(
                f"{self.supabase_url}/auth/v1/user",
                headers={"apikey": self.supabase_key,
                         "Authorization": f"Bearer {access_token}"},
                timeout=45)
        finally:
            if owned:
                client.close()
        if response.status_code == 401:
            raise SessionError("会话已失效（HTTP 401），请重新授权登录")
        if response.status_code >= 400:
            raise SessionError(f"校验会话失败（HTTP {response.status_code}）")
        try:
            payload = response.json()
        except ValueError:
            raise SessionError("校验会话失败：返回内容不是 JSON") from None
        if not isinstance(payload, dict) or not payload.get("id"):
            raise SessionError("校验会话失败：没有返回用户身份")
        return payload

    # -- 主流程 ---------------------------------------------------------
    def _with_retry(self, action, *, attempts: int = 2):
        """跑一次需要联网的校验步骤；只在瞬时网络故障时重试。

        每个 attempt 都用**新的客户端**：同一条已断的隧道重试没有意义。
        """
        last = None
        for attempt in range(attempts):
            client = self._client()
            try:
                return action(client)
            except SessionError:
                raise                      # 明确的业务失败不重试
            except Exception as exc:       # noqa: BLE001 — 判断后再决定是否重试
                last = exc
                if attempt + 1 >= attempts or not transient(exc):
                    raise SessionError(f"网络请求失败（代理隧道异常）：{type(exc).__name__}") from None
            finally:
                client.close()
        raise SessionError(f"网络请求失败：{type(last).__name__}")

    def validate(self, session: dict) -> dict:
        """返回校验后的会话（可能带刷新后的新 Token）与验证到的身份。

        只在本流程内按需刷新一次，不建立任何后台定时任务。
        """
        if not session.get("access_token") or not session.get("refresh_token"):
            raise SessionError("会话不完整：缺少 access_token 或 refresh_token")
        expected_id, expected_email = session["supabase_user_id"], session["email"]
        expired = int(session["expires_at"]) - EXPIRY_SKEW <= unix_now()
        if expired:
            # 刷新要用 refresh_token，失败就是真的失败，但隧道抖动仍可重试。
            payload = self._with_retry(lambda c: self.refresh(session, c))
            session = self.adopt(payload, session)
        user = self._with_retry(lambda c: self.whoami(session["access_token"], c))
        verified_id = user.get("id")
        if verified_id != expected_id:
            raise SessionError("会话归属不一致：验证到的用户 ID 与登录任务不符")
        verified_email = user.get("email")
        if isinstance(verified_email, str) and verified_email and verified_email != expected_email:
            raise SessionError("会话归属不一致：验证到的邮箱与登录任务不符")
        provider = (user.get("app_metadata") or {}).get("provider") \
            if isinstance(user.get("app_metadata"), dict) else None
        if provider and provider != AUTH_PROVIDER:
            raise SessionError(f"该账号的登录方式不是 GitHub（{provider}）")
        session = dict(session)
        session.update(supabase_user_id=verified_id,
                       email=verified_email or session["email"],
                       github_user_id=SessionExtractor.github_id(user) or
                       session.get("github_user_id"),
                       auth_provider=AUTH_PROVIDER)
        return session

    @staticmethod
    def adopt(payload: dict, previous: dict) -> dict:
        """把刷新结果合并进会话对象；refresh_token 轮换后必须一起更新。"""
        merged = dict(previous)
        merged["_refresh_payload"] = dict(payload)
        merged["access_token"] = payload["access_token"]
        merged["refresh_token"] = payload["refresh_token"]
        expires_at = payload.get("expires_at")
        if not isinstance(expires_at, (int, float)) or expires_at <= 0:
            expires_in = payload.get("expires_in")
            expires_at = unix_now() + int(expires_in or 3600)
        merged["expires_at"] = int(expires_at)
        if isinstance(payload.get("user"), dict) and payload["user"].get("id"):
            merged["user"] = payload["user"]
            merged["supabase_user_id"] = payload["user"]["id"]
            if isinstance(payload["user"].get("email"), str):
                merged["email"] = payload["user"]["email"]
        return merged
