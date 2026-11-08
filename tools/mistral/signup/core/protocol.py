"""
纯协议注册流程（不走浏览器 UI，只走 HTTP 接口）

价值:
  - 比 Playwright 快 10-20 倍（无渲染、无 JS 执行）
  - 资源占用极低，适合大规模批量
  - 结果可预测，无元素定位脆弱性

限制（实测 2026-11-08）:
  - **console.mistral.ai 和 chat.mistral.ai 有独立的 Cloudflare challenge**
    它们各发各的 cf_clearance，与 auth/admin 的不通用。
    纯协议访问这两个域会返回 403 "Just a moment..."
    => console 的 acceptTos、chat 的 acceptToS/generateApiKey 必须走浏览器
  - TLS 指纹 ≠ Chrome，CF 严格模式下 auth/admin 也可能被拦
  - 没有 Axept 行为事件

各域能力矩阵（实测 2026-11-08）:
  auth.mistral.ai    纯协议 ✅  注册 / 探测 / 发码 / 验码 全部可用
  admin.mistral.ai   纯协议 ✅  meSession / billing / api-keys / cancel / subscribe 全部可用
  console.mistral.ai 纯协议 ❌  403 Just a moment（CF 校验 TLS 指纹，curl 过不了）
  chat.mistral.ai    纯协议 ❌  403 Just a moment（同上）

=> 混合方案（推荐，已实现于 hybrid.py）:
   1. 浏览器进程常驻，负责 auth 首跳 + console/chat 的 CF 挑战
   2. 所有业务读写走协议（快）
   3. console/chat 的两个动作在浏览器内 evaluate 执行

   实测：即使浏览器先访问过 console/chat 并拿到 __cf_bm，
         curl 仍被拦，因为缺 cf_clearance 且 JA3 不匹配。

因此设计为：
  1. 先用浏览器过一次 CF，拿到 cf_clearance + ory_session
  2. 之后所有业务接口走纯 HTTP（curl_cffi 带上这套 cookie）
  链路: browser_provision() -> protocol_register()

实测下来所有业务接口都不需要浏览器：
  Kratos 注册/验证、admin 全部 tRPC、api-keys REST、
  console acceptTos、chat acceptToS、code-trpc generateApiKey
"""
from __future__ import annotations

import json
import random
import re
import time
import urllib.parse
from dataclasses import dataclass, field
from typing import Optional

AUTH = "https://auth.mistral.ai"
ADMIN = "https://admin.mistral.ai"
CONSOLE = "https://console.mistral.ai"
CHAT = "https://chat.mistral.ai"
RETURN_TO = f"{ADMIN}/subscriptions/upgrade/plans"


# ---------------------------------------------------------------- tRPC 编码
def trpc_query(json_body, meta_values: dict | None = None) -> str:
    """GET 型 tRPC 的 input 参数（需 urlencode）"""
    payload = {"json": json_body}
    if meta_values is not None:
        payload["meta"] = {"values": meta_values, "v": 1}
    elif json_body is None:
        payload["meta"] = {"values": ["undefined"], "v": 1}
    return urllib.parse.quote(json.dumps(payload, separators=(",", ":")))


def trpc_batch(json_body, meta_values: dict | None = None) -> dict:
    """POST 型 tRPC 的 batch body（batch=1）"""
    inner = {"json": json_body}
    if meta_values is not None:
        inner["meta"] = {"values": meta_values, "v": 1}
    elif json_body is None:
        inner["meta"] = {"values": ["undefined"], "v": 1}
    return {"0": inner}


def trpc_headers(csrf: str | None, streaming: bool = False) -> dict:
    h = {
        "Content-Type": "application/json",
        "x-trpc-source": "nextjs-react",
    }
    if streaming:
        h["trpc-accept"] = "application/jsonl"
    if csrf:
        h["x-csrftoken"] = csrf
    return h


def parse_trpc(text: str):
    """
    tRPC 响应可能是纯 JSON（batch=1 时为数组），
    也可能是 jsonl 流式（多行 JSON）。
    """
    text = text.strip()
    if not text:
        return None
    try:
        return json.loads(text)
    except Exception:
        pass
    out = []
    for line in text.split("\n"):
        line = line.strip()
        if not line:
            continue
        try:
            out.append(json.loads(line))
        except Exception:
            pass
    return out


# ---------------------------------------------------------------- 会话容器
@dataclass
class ProtocolSession:
    """一个账号的纯协议会话（携带浏览器产出的 cookie）"""
    cookies: dict = field(default_factory=dict)
    headers: dict = field(default_factory=dict)
    proxy: str = ""          # 本地 gateway 的 socks 地址

    def csrf(self, domain: str = "") -> Optional[str]:
        for k, v in self.cookies.items():
            if k == "csrftoken" or k.startswith("csrf_token"):
                return urllib.parse.unquote(v)
        return None

    def cookie_header(self) -> str:
        return "; ".join(f"{k}={v}" for k, v in self.cookies.items())


class ProtocolClient:
    """
    用系统 curl 子进程做传输层。

    为什么不用 curl_cffi / requests:
      - curl_cffi 在本机 TLS 库冲突（OPENSSL_internal:invalid library）
      - requests 不支持 preproxy 链式代理
      - 系统 curl 8.7.1 支持 --preproxy，且 TLS 栈稳定
    """

    def __init__(self, sess: "ProtocolSession", curl_bin: str = "curl",
                 timeout: int = 45):
        self.sess = sess
        self.curl = curl_bin
        self.timeout = timeout
        import tempfile, os
        self._jar = tempfile.NamedTemporaryFile(
            mode="w", suffix=".cookies", delete=False).name
        # 把已有 cookie 写入 jar
        lines = ["# Netscape HTTP Cookie File", ""]
        for k, v in sess.cookies.items():
            # __Host- 前缀的 cookie 必须是 host-only 且 path=/
            if k.startswith("__Host-"):
                lines.append("\t".join(["auth.mistral.ai", "FALSE", "/", "TRUE", "0", k, v]))
            else:
                lines.append("\t".join([".mistral.ai", "TRUE", "/", "TRUE", "0", k, v]))
        with open(self._jar, "w") as f:
            f.write("\n".join(lines) + "\n")

    def _run(self, method: str, url: str, headers: dict | None = None,
             body: str | None = None, params: dict | None = None) -> tuple[int, str]:
        import subprocess
        if params:
            import urllib.parse as up
            url = url + ("&" if "?" in url else "?") + up.urlencode(params)

        cmd = [self.curl, "-s", "-S", "-L", "-m", str(self.timeout),
               "--compressed",
               "-b", self._jar, "-c", self._jar]
        # proxy 为空表示直连（OVH 等海外机器可达目标站，无需代理）
        if self.sess.proxy:
            cmd += ["--socks5-hostname", self.sess.proxy.replace("socks5://", "")]
        # GET 时不加 -X，让 curl 正确处理 303 See Other -> GET
        if method.upper() != "GET":
            cmd += ["-X", method]

        hdrs = dict(self.sess.headers)
        # 该 curl 构建不支持 br/zstd，统一改 gzip
        hdrs["Accept-Encoding"] = "gzip, deflate"
        if headers:
            hdrs.update(headers)
        for k, v in hdrs.items():
            cmd += ["-H", f"{k}: {v}"]

        if body is not None:
            cmd += ["--data-binary", body]

        cmd += ["-w", "\n__STATUS__%{http_code}", url]

        r = subprocess.run(cmd, capture_output=True)
        out = r.stdout.decode("utf-8", errors="replace")
        status = 0
        if "__STATUS__" in out:
            body_part, _, st = out.rpartition("\n__STATUS__")
            try:
                status = int(st.strip())
            except Exception:
                status = 0
            return status, body_part
        return status, out

    def _apply_csrf(self, url: str) -> dict:
        import urllib.parse as up
        host = up.urlsplit(url).hostname or ""
        h = {}
        c = self.sess.csrf(host)
        if c:
            h["x-csrftoken"] = c
        return h

    def get(self, url, params=None, headers=None, **kw):
        h = self._apply_csrf(url)
        if headers:
            h.update(headers)
        st, body = self._run("GET", url, h, None, params)
        return Resp(st, body)

    def post(self, url, json=None, data=None, headers=None, params=None, **kw):
        h = self._apply_csrf(url)
        h.setdefault("Content-Type", "application/json")
        if headers:
            h.update(headers)
        body = None
        if json is not None:
            import json as _j
            body = _j.dumps(json, separators=(",", ":"))
        elif data is not None:
            body = data
        st, txt = self._run("POST", url, h, body, params)
        return Resp(st, txt)

    def delete(self, url, headers=None, **kw):
        h = self._apply_csrf(url)
        if headers:
            h.update(headers)
        st, body = self._run("DELETE", url, h)
        return Resp(st, body)

    def harvest_cookies(self):
        """curl 的 -c 已写回 jar，重读同步到 session"""
        try:
            with open(self._jar) as f:
                for line in f:
                    if line.startswith("#") or not line.strip():
                        continue
                    parts = line.rstrip("\n").split("\t")
                    if len(parts) >= 7:
                        self.sess.cookies[parts[5]] = parts[6]
        except Exception:
            pass

    def close(self):
        try:
            import os
            os.unlink(self._jar)
        except Exception:
            pass


class Resp:
    """极简响应对象，模仿 requests 的常用接口"""
    def __init__(self, status: int, text: str):
        self.status_code = status
        self.text = text or ""

    def json(self):
        import json as _j
        return _j.loads(self.text)

    def __repr__(self):
        return f"<Resp {self.status_code} {len(self.text)}b>"


# ---------------------------------------------------------------- Kratos
class KratosProtocol:
    def __init__(self, cli: ProtocolClient):
        self.c = cli

    @staticmethod
    def _csrf(flow: dict) -> str:
        for n in flow.get("ui", {}).get("nodes", []):
            a = n.get("attributes") or {}
            if a.get("name") == "csrf_token":
                return a["value"]
        raise RuntimeError("no csrf_token in flow")

    def probe(self, email: str) -> dict:
        """
        探测账号是否存在。

        实测（2026-11-08）：
          账号【存在】 -> 400，但 state=choose_method，nodes 里出现 password 节点
          账号【不存在】 -> 400，state 不变，nodes 里出现 4000037 错误消息
        所以不能只看状态码，要看 nodes 结构与 4000037。
        """
        r = self.c.get(f"{AUTH}/self-service/login/browser",
                       params={"return_to": RETURN_TO})
        flow = r.json()
        body = {"csrf_token": self._csrf(flow), "identifier": email,
                "method": "identifier_first", "transient_payload": {}}
        resp = self.c.post(f"{AUTH}/self-service/login",
                           params={"flow": flow["id"]}, json=body)
        self.c.harvest_cookies()

        j = {}
        try:
            j = resp.json()
        except Exception:
            pass

        nodes = j.get("ui", {}).get("nodes", [])
        node_types = {(n.get("group"), (n.get("attributes") or {}).get("name"))
                      for n in nodes}
        has_password = any(g == "password" or nm == "password"
                           for g, nm in node_types)

        not_found = False
        for n in nodes:
            for m in n.get("messages", []):
                if m.get("id") == 4000037:
                    not_found = True
        for m in j.get("ui", {}).get("messages", []):
            if m.get("id") == 4000037:
                not_found = True

        return {
            "exists": has_password and not not_found,
            "status": resp.status_code,
            "state": j.get("state"),
            "has_password_node": has_password,
            "not_found_error": not_found,
        }

    def register(self, email: str, password: str,
                 first: str = "", last: str = "") -> dict:
        r = self.c.get(f"{AUTH}/self-service/registration/browser",
                       params={"return_to": RETURN_TO,
                               "after_verification_return_to": RETURN_TO})
        flow = r.json()
        body = {
            "csrf_token": self._csrf(flow),
            "method": "password",
            "password": password,
            "traits": {"email": email, "name": {"first": first, "last": last}},
            "transient_payload": {},
        }
        resp = self.c.post(f"{AUTH}/self-service/registration",
                           params={"flow": flow["id"]}, json=body)
        self.c.harvest_cookies()
        j = resp.json() if resp.text else {}
        if resp.status_code != 200:
            msgs = []
            for n in j.get("ui", {}).get("nodes", []):
                for m in n.get("messages", []):
                    msgs.append(f"{m.get('type')}:{m.get('id')}:{m.get('text')}")
            for m in j.get("ui", {}).get("messages", []):
                msgs.append(f"UI:{m.get('type')}:{m.get('id')}:{m.get('text')}")
            raise RuntimeError(
                f"register failed {resp.status_code}: {' | '.join(msgs)[:400]}")
        sess = j.get("session") or {}
        return {
            "session_id": sess.get("id", ""),
            "identity_id": (sess.get("identity") or {}).get("id", ""),
        }

    def start_verification(self, email: str) -> dict:
        """
        发验证码，返回【可直接用于 submit_code 的 flow】。

        实测要点（2026-11-08）:
          1. GET /verification/browser 会 303，必须跟随重定向才能拿到 flow
          2. POST 提交 email 后，响应体本身就是新 flow：
             state=sent_email, active=code, nodes 含 code 输入框 + 轮换后的 csrf
          3. **必须直接用 POST 响应体**提交验证码
             - 再 GET 一次会重置 flow -> choose_method
             - 用旧 csrf 会 400 (4010006 Could not find a strategy)
        """
        r = self.c.get(f"{AUTH}/self-service/verification/browser")
        flow = r.json()
        body = {"csrf_token": self._csrf(flow), "email": email,
                "method": "code", "transient_payload": {}}
        resp = self.c.post(f"{AUTH}/self-service/verification",
                           params={"flow": flow["id"]}, json=body)
        self.c.harvest_cookies()
        try:
            return resp.json()
        except Exception:
            return flow

    def submit_code(self, flow: dict, code: str) -> dict:
        body = {"code": code, "csrf_token": self._csrf(flow),
                "method": "code", "transient_payload": {}}
        resp = self.c.post(f"{AUTH}/self-service/verification",
                           params={"flow": flow["id"]}, json=body)
        self.c.harvest_cookies()
        j = resp.json() if resp.text else {}
        return {"status": resp.status_code, "state": j.get("state"),
                "passed": j.get("state") == "passed_challenge"}


# ---------------------------------------------------------------- 业务层
class AdminProtocol:
    """admin.mistral.ai：身份、账单、订阅、Key"""

    def __init__(self, cli: ProtocolClient):
        self.c = cli

    # ---- 身份 ----
    def me_session(self) -> dict:
        url = f"{ADMIN}/api/local-trpc/user.meSession"
        r = self.c.get(url, params={"input": trpc_query(None)})
        try:
            return r.json()["result"]["data"]["json"] or {}
        except Exception:
            return {}

    def enterprise_type(self) -> dict:
        url = f"{ADMIN}/api/local-trpc/user.enterpriseAccountType"
        r = self.c.get(url, params={"input": trpc_query(None)})
        try:
            return r.json()["result"]["data"]["json"] or {}
        except Exception:
            return {}

    # ---- 账单 ----
    def billing_info(self) -> dict:
        url = f"{ADMIN}/api/local-trpc/billing.info"
        r = self.c.get(url, params={"input": trpc_query(None)})
        try:
            return r.json()["result"]["data"]["json"] or {}
        except Exception:
            return {}

    def update_info(self, info: dict) -> dict:
        url = f"{ADMIN}/api/local-trpc/billing.updateInfo"
        r = self.c.post(url, json=trpc_batch(info))
        return parse_trpc(r.text)

    def create_setup_intent(self) -> dict:
        url = f"{ADMIN}/api/local-trpc/billing.createPaymentMethodSetupIntent"
        r = self.c.post(url, json=trpc_batch(None))
        try:
            return parse_trpc(r.text)[0]["result"]["data"]["json"] or {}
        except Exception:
            return {}

    def list_payment_methods(self) -> list:
        url = f"{ADMIN}/api/local-trpc/billing.listPaymentMethods"
        r = self.c.get(url, params={"input": trpc_query(None)})
        try:
            return r.json()["result"]["data"]["json"] or []
        except Exception:
            return []

    # ---- 订阅 ----
    def get_subscription(self, plan_type: str = "API") -> Optional[dict]:
        url = f"{ADMIN}/api/local-trpc/billing.subscriptions.get"
        r = self.c.get(url, params={"input": trpc_query({"planType": plan_type})})
        try:
            return r.json()["result"]["data"]["json"]
        except Exception:
            return None

    def subscribe_lechat(self, plan: str = "INDIVIDUAL",
                         periodicity: str = "MONTHLY") -> dict:
        url = f"{ADMIN}/api/local-trpc/billing.subscribeLeChat"
        body = trpc_batch({"plan": plan, "periodicity": periodicity, "seats": None},
                          meta_values={"seats": ["undefined"]})
        r = self.c.post(url, json=body)
        try:
            return parse_trpc(r.text)[0]["result"]["data"]["json"] or {}
        except Exception:
            return {"raw": r.text[:300]}

    def cancel_subscription(self, plan_type: str = "API") -> dict:
        """
        唯一取消入口（实测）。
        planType: API | CHAT | ON_PREMISE | MISTRAL_CODE
        无订阅 -> 400 "No subscription in cancellation"
        """
        url = f"{ADMIN}/api/local-trpc/billing.subscriptions.cancelUnsubscription"
        r = self.c.post(url, json=trpc_batch({"planType": plan_type}))
        if r.status_code == 200:
            return {"ok": True, "body": parse_trpc(r.text)}
        return {"ok": False, "status": r.status_code, "body": r.text[:300]}

    # ---- API Key (REST，不是 tRPC) ----
    def create_api_key(self, workspace_uuid: str, product: str = "API",
                       name: str = "", scope: str = "personal_and_shared") -> dict:
        """
        product: API | Vibe | Studio
        返回含明文 key，只此一次
        """
        url = f"{ADMIN}/api/billing/api-keys"
        body = {
            "name": name or f"{product.lower()}-{random.randint(1000, 9999)}",
            "workspace_uuid": workspace_uuid,
            "primitive_access_scope": scope,
            "product": product,
        }
        r = self.c.post(url, json=body)
        try:
            return r.json()
        except Exception:
            return {"error": r.status_code, "raw": r.text[:300]}

    def list_api_keys(self, workspace_uuid: str | None = None) -> list:
        inp = {"status": "active", "limit": 30, "listAllOrgKeys": True,
               "listAllWsKeys": None, "workspaceId": workspace_uuid,
               "name": None, "sort": None, "order": None, "offset": 0}
        meta = {"listAllWsKeys": ["undefined"], "workspaceId": ["undefined"],
                "name": ["undefined"], "sort": ["undefined"], "order": ["undefined"]}
        url = f"{ADMIN}/api/local-trpc/albe.apiKeys.list"
        r = self.c.get(url, params={"input": trpc_query(inp, meta)})
        try:
            return r.json()["result"]["data"]["json"]["keys"]
        except Exception:
            return []

    def delete_api_key(self, key_id: str) -> dict:
        r = self.c.delete(f"{ADMIN}/api/billing/api-keys/{key_id}")
        try:
            return r.json()
        except Exception:
            return {"error": r.status_code}


class ConsoleProtocol:
    """console.mistral.ai：AI Studio ToS"""

    def __init__(self, cli: ProtocolClient):
        self.c = cli

    def accept_studio_tos(self) -> dict:
        """
        POST /api-ui/trpc/user.acceptTos?batch=1
        注意前缀是 /api-ui/trpc/，不是 /api/trpc/
        """
        url = f"{CONSOLE}/api-ui/trpc/user.acceptTos?batch=1"
        r = self.c.post(url, json=trpc_batch(None),
                        headers={"trpc-accept": "application/jsonl"})
        return {"status": r.status_code, "body": r.text[:200]}


class ChatProtocol:
    """chat.mistral.ai：Le Chat ToS、隐私开关、Vibe Key"""

    def __init__(self, cli: ProtocolClient):
        self.c = cli

    def accept_tos(self, newsletter: bool = True) -> dict:
        """POST /api/trpc/user.acceptToS?batch=1  {"json":{"newsletter":true}}"""
        url = f"{CHAT}/api/trpc/user.acceptToS?batch=1"
        r = self.c.post(url, json=trpc_batch({"newsletter": newsletter}))
        try:
            return parse_trpc(r.text)[0]["result"]["data"]["json"]
        except Exception:
            return {"status": r.status_code, "raw": r.text[:200]}

    def set_message_training(self, enabled: bool = False) -> dict:
        """隐私：禁止用消息训练模型（批量注册的合规姿态）"""
        url = f"{CHAT}/api/trpc/user.setMessageTrainingUse?batch=1"
        r = self.c.post(url, json=trpc_batch({"enabled": enabled}))
        return {"status": r.status_code}

    def set_knowledge(self, enabled: bool = False) -> dict:
        url = f"{CHAT}/api/trpc/user.setKnowledgeEnabled?batch=1"
        r = self.c.post(url, json=trpc_batch({"enabled": enabled}))
        return {"status": r.status_code}

    def set_localisation(self, consent: bool = False) -> dict:
        url = f"{CHAT}/api/trpc/user.setLocalisationConsent?batch=1"
        r = self.c.post(url, json=trpc_batch({"consent": consent}))
        return {"status": r.status_code}

    def generate_vibe_key(self) -> dict:
        """
        POST /api/code-trpc/apiKey.generateApiKey?batch=1
        注意前缀是 /api/code-trpc/（不是 /api/trpc/）
        返回 {"vibeApiKey":"mstrl_...","vibeApiKeyId":"..."}
        """
        url = f"{CHAT}/api/code-trpc/apiKey.generateApiKey?batch=1"
        r = self.c.post(url, json=trpc_batch(None))
        try:
            data = parse_trpc(r.text)
            if isinstance(data, list):
                return data[0]["result"]["data"]["json"]
            return data
        except Exception:
            return {"status": r.status_code, "raw": r.text[:300]}

    def session(self) -> dict:
        url = f"{CHAT}/api/trpc/user.session"
        r = self.c.get(url)
        try:
            return r.json()["result"]["data"]["json"]
        except Exception:
            return {}


# ---------------------------------------------------------------- 编排
def protocol_full_signup(cli: ProtocolClient, email: str, password: str,
                         first: str, last: str, code: str,
                         do_tos: bool = True, do_privacy: bool = True
                         ) -> dict:
    """
    纯协议完整注册。前提: cli 已带浏览器产出的 cf_clearance + ory_session。
    code 需要外部提供（接码）。
    """
    out: dict = {"email": email}
    k = KratosProtocol(cli)
    a = AdminProtocol(cli)
    c = ConsoleProtocol(cli)
    ch = ChatProtocol(cli)

    # 1. 注册
    reg = k.register(email, password, first, last)
    out["identity_id"] = reg["identity_id"]
    out["session_id"] = reg["session_id"]

    # 2. 验证
    flow = k.start_verification(email)
    ver = k.submit_code(flow, code)
    out["verified"] = ver["passed"]

    # 3. 接受 ToS（两处都要）
    if do_tos:
        out["studio_tos"] = c.accept_studio_tos()
        out["chat_tos"] = ch.accept_tos(newsletter=True)

    # 4. 隐私开关（降低风险画像）
    if do_privacy:
        ch.set_message_training(False)
        ch.set_knowledge(False)
        ch.set_localisation(False)

    # 5. 取身份锚点
    me = a.me_session()
    out["org_uuid"] = (me.get("organization") or {}).get("uuid")
    out["workspace_uuid"] = (me.get("workspace") or {}).get("uuid")

    # 6. 三把 Key
    out["keys"] = {}
    if out["workspace_uuid"]:
        for prod in ("API", "Vibe", "Studio"):
            r = a.create_api_key(out["workspace_uuid"], product=prod)
            out["keys"][prod] = r.get("key", r)

    return out
