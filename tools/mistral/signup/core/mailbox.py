"""
iCloud HME 接码

接口（实测确认）:
    GET https://icloud.mida.vip/api/messages
        ?alias_email=<邮箱，原文 @ 也可以，会做 URL 编码>
        &token=<分享 token>
        &range=all|latest|3d
        [&start_time=<ISO>]

返回:
    {"ok":true,"data":{"aliasEmail":"...","expiresAt":"...","shareStatus":"active",
      "emails":[{"id":...,"fromAddress":"...","subject":"...","textContent":"..."}]}}

坑:
  - 参数名是 alias_email，不是 email（用错会 401 "分享链接无效或已失效"）
  - range 必填，否则按 3d
  - 分享链接里的 token 和邮箱必须是同一对
"""
from __future__ import annotations

import re
import time
import urllib.parse
from dataclasses import dataclass
from typing import Callable, Optional

import requests


API = "https://icloud.mida.vip/api/messages"


@dataclass
class Mailbox:
    email: str
    url: str          # 完整分享链接
    token: str = ""

    def __post_init__(self):
        if not self.token and "token=" in self.url:
            q = urllib.parse.urlparse(self.url).query
            self.token = urllib.parse.parse_qs(q).get("token", [""])[0]
        if not self.email and "/messages/" in self.url:
            seg = self.url.split("/messages/")[1].split("?")[0]
            self.email = urllib.parse.unquote(seg)

    @classmethod
    def from_line(cls, line: str) -> "Mailbox":
        """解析 "邮箱----链接" 格式"""
        parts = line.strip().split("----")
        return cls(email=parts[0].strip(), url=parts[1].strip())


class ICloudMailbox:
    """轮询收件箱提取验证码"""

    def __init__(self, mailbox: Mailbox, proxy: Optional[str] = None,
                 timeout: int = 20):
        self.mb = mailbox
        self.proxy = proxy
        self.timeout = timeout
        self.s = requests.Session()
        if proxy:
            self.s.proxies = {"http": proxy, "https": proxy}

    def fetch(self, rng: str = "all") -> dict:
        params = {
            "alias_email": self.mb.email,
            "token": self.mb.token,
            "range": rng,
        }
        r = self.s.get(API, params=params, timeout=self.timeout)
        try:
            j = r.json()
        except Exception:
            return {"ok": False, "error": f"bad json: {r.text[:200]}"}
        return j

    def wait_for_code(self, since_ts: Optional[str] = None,
                      timeout_s: int = 180, interval_s: int = 5,
                      pattern: str = r"\b(\d{6})\b",
                      subject_hint: str = "mistral") -> Optional[str]:
        """
        轮询等待验证码。
        since_ts: ISO 时间，只认之后到达的邮件（避免拿到旧邮件）
        """
        deadline = time.time() + timeout_s
        seen_ids = set()

        while time.time() < deadline:
            j = self.fetch("all")
            if not j.get("ok"):
                time.sleep(interval_s)
                continue

            emails = (j.get("data") or {}).get("emails") or []
            for e in emails:
                eid = e.get("id")
                if eid in seen_ids:
                    continue
                seen_ids.add(eid)

                subject = (e.get("subject") or "").lower()
                body = e.get("textContent") or e.get("htmlContent") or ""

                if subject_hint and subject_hint not in subject and subject_hint not in body.lower():
                    continue

                m = re.search(pattern, body)
                if m:
                    return m.group(1)

            time.sleep(interval_s)
        return None

    def last_emails(self, n: int = 5) -> list[dict]:
        j = self.fetch("all")
        if not j.get("ok"):
            return []
        emails = (j.get("data") or {}).get("emails") or []
        return emails[:n]


def load_mailboxes(path: str) -> list[Mailbox]:
    """从文件加载 "邮箱----链接" 列表"""
    out = []
    with open(path, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line or line.startswith("#"):
                continue
            if "----" in line:
                out.append(Mailbox.from_line(line))
    return out
