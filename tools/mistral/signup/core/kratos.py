"""
Ory Kratos 注册流程（纯 HTTP 复刻）

依据实测抓包：
  1. POST /self-service/login          identifier_first 探测账号
  2. GET  /self-service/registration/browser    创建注册流
  3. POST /self-service/registration?flow=X     提交密码
  4. GET  /self-service/verification/browser    拉验证流
  5. POST /self-service/verification?flow=X     提交验证码

关键约束：
  - csrf_token 每个 flow 独立，POST 后轮换
  - 必须带 Referer: https://v2.auth.mistral.ai/
  - Origin 必须是 https://v2.auth.mistral.ai
"""
from __future__ import annotations

import json
import time
import urllib.parse
from dataclasses import dataclass, field
from typing import Any, Optional

import requests

BASE = "https://auth.mistral.ai"
ORIGIN = "https://v2.auth.mistral.ai"
RETURN_TO = "https://admin.mistral.ai/subscriptions/upgrade/plans"


class KratosError(Exception):
    def __init__(self, stage: str, status: int, body: Any):
        self.stage = stage
        self.status = status
        self.body = body
        super().__init__(f"[{stage}] HTTP {status}: {json.dumps(body, ensure_ascii=False)[:300]}")


@dataclass
class KratosSession:
    session_id: str
    identity_id: str
    email: str
    expires_at: str
    raw: dict = field(default_factory=dict)


def make_session(headers: dict[str, str], proxy_url: str, pre_proxy: str,
                 timeout: int = 45) -> requests.Session:
    """
    构造一个走链式代理的 requests 会话。

    requests 不能直接做 preproxy，这里用 monkeypatch:
    把 urllib3 的 SOCKS 连接先经 pre_proxy。
    实际实现用 requests + 自定义 adapter（见 core.chained_http）。
    """
    from core.chained_http import ChainedHTTPAdapter

    s = requests.Session()
    s.headers.update(headers)
    adapter = ChainedHTTPAdapter(pre_proxy=pre_proxy, target_proxy=proxy_url)
    s.mount("https://", adapter)
    s.mount("http://", adapter)
    return s


class KratosFlow:
    def __init__(self, http: requests.Session, sleep_range: tuple[float, float] = (0.8, 3.5)):
        self.http = http
        self.sleep_range = sleep_range

    # ---------- 工具 ----------
    def _pause(self):
        import random
        time.sleep(random.uniform(*self.sleep_range))

    @staticmethod
    def _csrf(flow: dict) -> str:
        for n in flow.get("ui", {}).get("nodes", []):
            a = n.get("attributes") or {}
            if a.get("name") == "csrf_token":
                return a["value"]
        raise KratosError("csrf", 0, {"msg": "no csrf_token in flow"})

    @staticmethod
    def _errors(flow: dict) -> list[str]:
        out = []
        for m in flow.get("ui", {}).get("messages", []):
            out.append(f"{m.get('type')}: {m.get('text')}")
        for n in flow.get("ui", {}).get("nodes", []):
            for m in n.get("messages", []):
                out.append(f"node[{m.get('type')}]: {m.get('text')} (id={m.get('id')})")
        return out

    # ---------- 1. 探测 ----------
    def probe(self, email: str) -> dict:
        r = self.http.get(f"{BASE}/self-service/login/browser", params={"return_to": RETURN_TO})
        if r.status_code != 200:
            raise KratosError("login_browser", r.status_code, r.text[:400])
        flow = r.json()

        payload = {
            "csrf_token": self._csrf(flow),
            "identifier": email,
            "method": "identifier_first",
            "transient_payload": {},
        }
        self._pause()
        resp = self.http.post(f"{BASE}/self-service/login",
                              params={"flow": flow["id"]}, json=payload)
        body = resp.json() if resp.text else {}
        exists = resp.status_code == 200
        return {
            "exists": exists,
            "status": resp.status_code,
            "errors": self._errors(body) if not exists else [],
            "body": body,
        }

    # ---------- 2+3. 注册 ----------
    def register(self, email: str, password: str) -> KratosSession:
        r = self.http.get(f"{BASE}/self-service/registration/browser",
                          params={"return_to": RETURN_TO,
                                  "after_verification_return_to": RETURN_TO})
        if r.status_code != 200:
            raise KratosError("registration_browser", r.status_code, r.text[:400])
        flow = r.json()

        payload = {
            "csrf_token": self._csrf(flow),
            "method": "password",
            "password": password,
            "traits": {"email": email, "name": {"first": "", "last": ""}},
            "transient_payload": {},
        }
        self._pause()
        resp = self.http.post(f"{BASE}/self-service/registration",
                              params={"flow": flow["id"]}, json=payload)
        body = resp.json() if resp.text else {}

        if resp.status_code != 200:
            raise KratosError("registration", resp.status_code,
                              {"errors": self._errors(body), "raw": str(body)[:500]})

        sess = body.get("session") or {}
        ident = sess.get("identity") or {}
        return KratosSession(
            session_id=sess.get("id", ""),
            identity_id=ident.get("id", ""),
            email=email,
            expires_at=sess.get("expires_at", ""),
            raw=body,
        )

    # ---------- 4. 拉验证流 ----------
    def get_verification_flow(self) -> dict:
        r = self.http.get(f"{BASE}/self-service/verification/browser")
        if r.status_code != 200:
            raise KratosError("verification_browser", r.status_code, r.text[:400])
        return r.json()

    # ---------- 5. 提交验证码 ----------
    def verify_code(self, flow: dict, code: str) -> dict:
        payload = {
            "code": code,
            "csrf_token": self._csrf(flow),
            "method": "code",
            "transient_payload": {},
        }
        self._pause()
        resp = self.http.post(f"{BASE}/self-service/verification",
                              params={"flow": flow["id"]}, json=payload)
        body = resp.json() if resp.text else {}
        return {
            "status": resp.status_code,
            "state": body.get("state"),
            "passed": body.get("state") == "passed_challenge",
            "errors": self._errors(body),
            "body": body,
        }

    # ---------- 重发验证码 ----------
    def resend_code(self, flow: dict) -> dict:
        payload = {
            "csrf_token": self._csrf(flow),
            "method": "code",
            "email": flow.get("request_url", ""),
            "transient_payload": {},
        }
        self._pause()
        resp = self.http.post(f"{BASE}/self-service/verification",
                              params={"flow": flow["id"]}, json=payload)
        return {"status": resp.status_code, "body": resp.json() if resp.text else {}}


def find_code_in_mail(text: str) -> Optional[str]:
    """
    从邮件正文提取 6 位验证码。
    Mistral 的邮件格式: "Your verification code is 123456" 之类。
    """
    import re
    # 优先找上下文明确的
    patterns = [
        r"(?:verification|verify|code|Code)\D{0,30}(\d{6})\b",
        r"\b(\d{6})\b",
    ]
    for p in patterns:
        m = re.search(p, text)
        if m:
            return m.group(1)
    return None
