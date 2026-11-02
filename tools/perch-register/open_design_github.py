from __future__ import annotations

import argparse
import base64
import hashlib
import hmac
import json
import os
import re
import struct
import sys
import time
from dataclasses import dataclass
from datetime import datetime, timezone
from html import unescape
from pathlib import Path
from typing import Any, Callable
from urllib.parse import urljoin, urlsplit
from uuid import uuid4

from curl_cffi import CurlOpt, requests as curl_requests
from logicc_protocol import NetworkError, Pacer, ProxyConfig
GITHUB = "https://github.com"
GITHUB_HOSTS = {"github.com"}
OAUTH_CALLBACK = "https://open-design.ai/cloud/dashboard"
SEPARATOR = re.compile(r"-{2,}")
CODE_PATTERN = r"(?<![0-9A-Za-z])([0-9]{6,8})(?![0-9A-Za-z])"
BASE32 = re.compile(r"[A-Z2-7]{16,}")
CLOUDFLARE_MAIL = re.compile(r'data-cfemail="([0-9a-fA-F]+)"')
RETRYABLE = (429, 500, 502, 503, 504)


class GithubAuthError(RuntimeError):
    """GitHub 侧无法自动完成（凭据、2FA、设备验证或页面结构变化）。"""
@dataclass(frozen=True)
class GithubAccount:
    login: str
    password: str
    totp_secret: str
    mailbox_url: str

    def __post_init__(self) -> None:
        if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9-]{0,38}", self.login):
            raise ValueError(f"GitHub 账号名无效：{self.login!r}")
        if not isinstance(self.password, str) or not self.password:
            raise ValueError("GitHub 密码不能为空")
        secret = self.totp_secret.replace(" ", "").upper()
        if not BASE32.fullmatch(secret):
            raise ValueError(f"2FA 密钥不是有效 Base32：{self.totp_secret!r}")
        object.__setattr__(self, "totp_secret", secret)
        parts = urlsplit(self.mailbox_url)
        if parts.scheme not in {"http", "https"} or not parts.hostname or not parts.path.strip("/"):
            raise ValueError(f"接码页面地址无效：{self.mailbox_url!r}")

def totp(secret: str, at: float | None = None) -> str:
    """RFC 6238：SHA1 / 6 位 / 30 秒。"""
    key = base64.b32decode(secret.upper() + "=" * (-len(secret) % 8))
    counter = int((at if at is not None else time.time()) // 30)
    digest = hmac.new(key, struct.pack(">Q", counter), hashlib.sha1).digest()
    offset = digest[-1] & 0x0F
    return f"{(struct.unpack('>I', digest[offset:offset + 4])[0] & 0x7FFFFFFF) % 1000000:06d}"


def totp_candidates(secret: str) -> list[str]:
    now = time.time()
    values: list[str] = []
    for offset in (0, 30, -30):
        value = totp(secret, now + offset)
        if value not in values:
            values.append(value)
    return values


# ---------------------------------------------------------- 接码页面解析


def decode_cloudflare(hex_value: str) -> str:
    raw = bytes.fromhex(hex_value)
    if not raw:
        return ""
    key = raw[0]
    return bytes(byte ^ key for byte in raw[1:]).decode("utf-8", "replace")


def strip_tags(value: str) -> str:
    return unescape(re.sub(r"<[^>]+>", " ", value)).strip()


@dataclass(frozen=True)
class Mail:
    subject: str
    sender: str
    recipient: str
    sent_at: str
    body: str

    @property
    def message_id(self) -> str:
        digest = hashlib.sha256(f"{self.subject}\n{self.sent_at}\n{self.body}".encode()).hexdigest()
        return digest[:32]

    def codes(self) -> list[str]:
        ranked: list[str] = []
        for line in self.body.splitlines():
            if re.search(r"code", line, re.IGNORECASE):
                ranked.extend(re.findall(CODE_PATTERN, line))
        ranked.extend(re.findall(CODE_PATTERN, self.body))
        ordered: list[str] = []
        for value in ranked:
            if len(value) in (6, 7, 8) and value not in ordered:
                ordered.append(value)
        return ordered


def parse_mailbox(html: str) -> Mail | None:
    """MailCat 页面只渲染最新一封邮件；无邮件时返回 None。"""
    if 'class="panel"' not in html:
        return None
    subject = re.search(r'<div class="subject">(.*?)</div>', html, re.S)
    rows = dict(re.findall(r'<div class="label">([^<]+)</div><div class="value">(.*?)</div>', html, re.S))
    content = re.search(r'<div class="content">(.*?)</div>\s*</section>', html, re.S)

    def address(raw: str | None) -> str:
        if not raw:
            return ""
        protected = CLOUDFLARE_MAIL.search(raw)
        if protected:
            try:
                return decode_cloudflare(protected.group(1))
            except ValueError:
                return ""
        return strip_tags(raw)

    body = content.group(1) if content else ""
    body = re.sub(r"</(?:p|div|pre|tr|h[1-6])>", "\n", body)
    body = re.sub(r"<br\s*/?>", "\n", body)
    body = unescape(re.sub(r"<[^>]+>", "", body)).strip()
    return Mail(subject=strip_tags(subject.group(1)) if subject else "",
                sender=address(rows.get("发件人")),
                recipient=address(rows.get("收件人")),
                sent_at=strip_tags(rows.get("时间", "")),
                body=body)


class MailboxReader:
    """只读轮询接码平台的邮箱页面。"""

    def __init__(self, url: str, pacer: Pacer, session: Any = None, *, proxy: ProxyConfig | None = None):
        parts = urlsplit(url)
        if parts.scheme not in {"http", "https"} or not parts.hostname:
            raise ValueError("接码页面地址无效")
        self.url, self.pacer = url, pacer
        proxy_url = proxy.request_url if proxy else None
        self.session = session or curl_requests.Session(
            impersonate="chrome", proxy=proxy_url, trust_env=False,
            curl_options={CurlOpt.PROXY: proxy_url or "", CurlOpt.NOPROXY: ""})
        self.owner = session is None

    def close(self) -> None:
        if self.owner:
            self.session.close()

    def fetch(self) -> Mail | None:
        response = self.session.get(self.url, timeout=30, allow_redirects=True,
                                    headers={"Accept": "text/html", "Cache-Control": "no-cache"})
        if response.status_code != 200:
            raise NetworkError(f"接码页面返回 HTTP {response.status_code}")
        return parse_mailbox(response.text)

    def wait_for_code(self, *, timeout: float, interval: float,
                      match: Callable[[Mail], bool] | None = None) -> tuple[str, Mail]:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            mail = self.fetch()
            if mail is not None and (match is None or match(mail)):
                codes = mail.codes()
                if codes:
                    return codes[0], mail
            self.pacer.pause("等待邮件到达", min(interval, max(0.0, deadline - time.monotonic())))
        raise TimeoutError("接码页面等待邮件超时；请确认邮箱链接仍然有效")


# ------------------------------------------------------------- GitHub 侧


class GithubBrowser:
    """GitHub 网页登录：密码、TOTP 2FA、设备验证与 OAuth 授权确认。"""

    def __init__(self, pacer: Pacer, proxy: ProxyConfig | None = None, session: Any = None,
                 mailbox: MailboxReader | None = None, *, totp_secret: str = ""):
        proxy_url = proxy.request_url if proxy else None
        self.pacer, self.mailbox, self.totp_secret = pacer, mailbox, totp_secret
        self.session = session or curl_requests.Session(
            impersonate="chrome", proxy=proxy_url, trust_env=False,
            curl_options={CurlOpt.PROXY: proxy_url or "", CurlOpt.NOPROXY: ""},
            headers={"Accept-Language": "en-US,en;q=0.9"})
        self.logged_in = False

    def close(self) -> None:
        self.session.close()

    def request(self, method: str, url: str, **kwargs: Any):
        parts = urlsplit(url)
        if parts.scheme != "https" or parts.hostname not in GITHUB_HOSTS:
            raise GithubAuthError(f"拒绝请求非 GitHub 地址：{url}")
        kwargs.setdefault("timeout", 45)
        return self.session.request(method, url, **kwargs)

    # -- 解析 -----------------------------------------------------------
    @staticmethod
    def _inputs(html: str) -> dict[str, str]:
        """按标签解析表单字段，不依赖 name/value 的先后顺序。

        GitHub 的 OAuth 授权页把「Authorize」做成
        `<button type="submit" name="authorize" value="...">`，只扫 `<input>`
        会漏掉它——授权页因此被误判成「结构已变化」。按钮也一并解析。
        """
        fields: dict[str, str] = {}
        for tag in re.findall(r"<(?:input|button)[^>]*>", html):
            name = re.search(r'\bname="([^"]*)"', tag)
            if not name:
                continue
            value = re.search(r'\bvalue="([^"]*)"', tag)
            fields[unescape(name.group(1))] = unescape(value.group(1)) if value else ""
        return fields

    @staticmethod
    def _csrf(html: str) -> str:
        match = re.search(r'name="authenticity_token"[^>]*value="([^"]+)"', html) or \
            re.search(r'value="([^"]+)"[^>]*name="authenticity_token"', html)
        if not match:
            raise GithubAuthError("GitHub 页面缺少 authenticity_token（页面结构已变化）")
        return unescape(match.group(1))

    @staticmethod
    def _flash(html: str) -> str:
        match = re.search(r'(?:flash-error|flash-warn)[^>]*>\s*(?:<[^>]+>\s*)*([^<]{4,200})', html)
        return unescape(match.group(1)).strip() if match else ""

    @staticmethod
    def _text(html: str) -> str:
        value = re.sub(r"<(script|style)[^>]*>.*?</\1>", " ", html, flags=re.S)
        return re.sub(r"\s+", " ", unescape(re.sub(r"<[^>]+>", " ", value))).strip()

    # -- 登录 -----------------------------------------------------------
    def sign_in(self, account: GithubAccount) -> None:
        page = self.request("GET", f"{GITHUB}/login")
        if page.status_code != 200:
            raise GithubAuthError(f"GitHub 登录页返回 HTTP {page.status_code}")
        fields = self._inputs(page.text)
        if "authenticity_token" not in fields:
            raise GithubAuthError("GitHub 登录页结构已变化（缺少 CSRF 令牌）")
        data = {
            "authenticity_token": fields["authenticity_token"],
            "login": account.login,
            "password": account.password,
            "webauthn-conditional": "undefined",
            "javascript-support": "unknown",
            "webauthn-support": "supported",
            "webauthn-iuvpaa-support": "unsupported",
            "return_to": f"{GITHUB}/login",
            "commit": "Sign in",
        }
        for key in ("timestamp", "timestamp_secret"):
            if fields.get(key):
                data[key] = fields[key]
        self.pacer.pause("GitHub 密码登录前")
        response = self._post(f"{GITHUB}/session", data,
                              referer=f"{GITHUB}/login", expect_redirect=True)
        if response.status_code == 200:
            raise GithubAuthError(
                f"GitHub 拒绝密码登录：{self._flash(response.text) or self._text(response.text)[:120]}")
        self._after_login(response.headers.get("location", ""))

    def _post(self, url: str, data: dict, *, referer: str, expect_redirect: bool = False):
        """POST 表单；遇到 429/5xx 按 Retry-After 重试一次。"""
        for attempt in range(2):
            response = self.request("POST", url, data=data, allow_redirects=False,
                                    headers={"Referer": referer, "Origin": GITHUB})
            if response.status_code in RETRYABLE and attempt == 0:
                delay = 30.0
                header = response.headers.get("Retry-After")
                if header and header.isdigit():
                    delay = min(120.0, max(15.0, float(header)))
                self.pacer.pause(f"GitHub 返回 HTTP {response.status_code}，等待后重试", delay)
                continue
            return response
        raise AssertionError("unreachable")

    def _after_login(self, location: str) -> None:
        target = self._absolute(location)
        for _ in range(4):
            if "/sessions/two-factor" in target:
                target = self._two_factor()
                continue
            if "/sessions/verified-device" in target:
                target = self._device_verification(target)
                continue
            self.logged_in = True
            return
        raise GithubAuthError("GitHub 登录后进入未知的校验循环")

    @staticmethod
    def _absolute(location: str) -> str:
        if not location:
            raise GithubAuthError("GitHub 未返回跳转地址")
        return location if location.startswith("https://") else urljoin(GITHUB, location)

    def _two_factor(self) -> str:
        page = self.request("GET", f"{GITHUB}/sessions/two-factor/app")
        if page.status_code != 200:
            raise GithubAuthError(f"2FA 页面返回 HTTP {page.status_code}")
        if 'name="authenticity_token"' not in page.text:
            raise GithubAuthError("2FA 页面结构已变化（缺少 CSRF 令牌）")
        token = self._csrf(page.text)
        last = ""
        for code in totp_candidates(self.totp_secret):
            self.pacer.pause("提交 GitHub 2FA 验证码前")
            response = self._post(f"{GITHUB}/sessions/two-factor",
                                  {"authenticity_token": token, "app_otp": code},
                                  referer=f"{GITHUB}/sessions/two-factor/app")
            if response.status_code in (301, 302, 303):
                return self._absolute(response.headers.get("location", ""))
            if response.status_code in RETRYABLE:
                raise GithubAuthError(
                    f"GitHub 2FA 接口限流（HTTP {response.status_code}）；请稍后重试该账号")
            last = self._flash(response.text) or f"HTTP {response.status_code}"
        raise GithubAuthError(f"GitHub 2FA 验证码被拒绝：{last}")

    def _device_verification(self, url: str) -> str:
        if self.mailbox is None:
            raise GithubAuthError("GitHub 要求设备验证邮件，但未提供接码页面")
        page = self.request("GET", url)
        if page.status_code != 200:
            raise GithubAuthError(f"设备验证页面返回 HTTP {page.status_code}")
        fields = self._inputs(page.text)
        token = fields.get("authenticity_token") or self._csrf(page.text)
        name = next((key for key in fields
                     if key != "authenticity_token" and re.search(r"(code|otp)", key, re.I)),
                    "verification_code")
        try:
            code, mail = self.mailbox.wait_for_code(
                timeout=180, interval=5,
                match=lambda item: "github" in item.sender.lower())
        except TimeoutError as exc:
            raise GithubAuthError(f"未从接码页面取到设备验证码：{exc}") from None
        self.pacer.pause("提交 GitHub 设备验证码前")
        response = self._post(f"{GITHUB}/sessions/verified-device",
                              {"authenticity_token": token, name: code}, referer=url)
        if response.status_code in (301, 302, 303):
            return self._absolute(response.headers.get("location", ""))
        raise GithubAuthError(f"GitHub 拒绝设备验证码：{self._flash(response.text) or '未知原因'}")
