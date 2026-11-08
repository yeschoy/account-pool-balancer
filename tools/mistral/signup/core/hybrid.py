"""
混合注册机：浏览器过 CF + 协议跑业务

为什么必须混合（实测 2026-11-08）:
  auth.mistral.ai    协议可用 ✅
  admin.mistral.ai   协议可用 ✅
  console.mistral.ai 协议 403 ❌（CF 校验 JA3，curl 过不了）
  chat.mistral.ai    协议 403 ❌（同上）

所以：
  - 注册/验证/身份/账单/Key/取消  -> 纯协议（快）
  - Studio ToS / Chat ToS / Vibe CLI Key -> 浏览器内 evaluate

性能对比（单账号，实测）:
  全浏览器路径  ~95-140 秒
  混合路径      ~55-70 秒（浏览器生命周期缩短，且很多步骤并发）
  全协议路径    ~25 秒（但 console/chat 不可用）
"""
from __future__ import annotations

import json
import random
import re
import time
from dataclasses import dataclass, field
from typing import Optional

from core.browser import BrowserOptions, SignupBrowser
from core.chain_gateway import GatewayPool, find_working_proxy
from core.fingerprint import billing_address, build_profile
from core.mailbox import ICloudMailbox, Mailbox
from core.protocol import (
    AdminProtocol, ChatProtocol, ConsoleProtocol, KratosProtocol,
    ProtocolClient, ProtocolSession, parse_trpc, trpc_batch,
)
from core.proxy_pool import DEFAULT_SIDS, ProxyPool


# ---------------------------------------------------------------- 浏览器侧动作
def browser_accept_studio_tos(page) -> dict:
    """console.mistral.ai 的 ToS —— 必须在浏览器里做（CF）"""
    page.goto("https://console.mistral.ai/plateforme/studio",
              wait_until="domcontentloaded", timeout=60000)
    page.wait_for_timeout(6000)
    body = page.locator("body").inner_text()
    if "Accept and continue" not in body:
        return {"ok": True, "already": True}
    try:
        page.get_by_role("button", name="Accept and continue").first.click(timeout=10000)
        page.wait_for_timeout(5000)
        return {"ok": True, "clicked": True}
    except Exception as e:
        return {"ok": False, "error": str(e)[:100]}


def browser_chat_actions(page) -> dict:
    """
    chat.mistral.ai 全流程——必须在浏览器里做（CF 拦截协议访问）。

    实测顺序（2026-11-08）:
      1. 打开 /code/extensions，页面会弹 Vibe ToS
         -> 点 "Accept and continue"
         -> 内部调用 user.acceptToS with {"newsletter": false}
      2. 展开 "Advanced" 才露出 Vibe API Key 区域
      3. 点 "Generate API Key"
         -> POST /api/code-trpc/apiKey.generateApiKey?batch=1
         -> 返回 vibeApiKey / vibeApiKeyId

    坑 1: 没接受 Vibe ToS 就调 generateApiKey 会 400 "Bad request"
    坑 2: **账号必须先完成 billing setup**（有过支付方式）
          实测 is_billing_setup=False 的账号，即使 ToS 已接受，
          generateApiKey 仍固定返回 400 Bad request。
          判定方法: admin 的 billing.info -> is_billing_setup
    """
    page.goto("https://chat.mistral.ai/code/extensions",
              wait_until="domcontentloaded", timeout=60000)
    page.wait_for_timeout(9000)

    out: dict = {}

    # ---- 1. 接受 Vibe ToS ----
    accepted = False
    for _ in range(3):
        try:
            btn = page.get_by_role("button", name="Accept and continue").first
            if btn.count():
                btn.click(timeout=8000)
                page.wait_for_timeout(6000)
                accepted = True
                continue
            break
        except Exception:
            break
    out["vibe_tos_accepted"] = accepted

    # 补一次显式 acceptToS（newsletter=false，与页面一致）
    try:
        r = page.evaluate("""async () => {
            const res = await fetch('/api/trpc/user.acceptToS?batch=1', {
                method:'POST', credentials:'include',
                headers:{'Content-Type':'application/json','x-trpc-source':'nextjs-react'},
                body: JSON.stringify({"0":{json:{newsletter:false}}})});
            return JSON.stringify({s:res.status, b:(await res.text()).slice(0,200)});
        }""")
        out["tos_call"] = json.loads(r).get("s")
    except Exception as e:
        out["tos_call"] = str(e)[:60]

    # ---- 2. 隐私开关 ----
    def _post(proc, body):
        try:
            return json.loads(page.evaluate("""async ([proc, body]) => {
                const res = await fetch('/api/trpc/'+proc+'?batch=1', {
                    method:'POST', credentials:'include',
                    headers:{'Content-Type':'application/json','x-trpc-source':'nextjs-react'},
                    body: JSON.stringify({"0":{json:body}})});
                return JSON.stringify({s:res.status});
            }""", [proc, body])).get("s")
        except Exception:
            return None

    out["training"] = _post("user.setMessageTrainingUse", {"enabled": False})
    out["knowledge"] = _post("user.setKnowledgeEnabled", {"enabled": False})
    out["localisation"] = _post("user.setLocalisationConsent", {"consent": False})

    # ---- 3. 展开 Advanced ----
    try:
        adv = page.get_by_role("button", name="Advanced").first
        if adv.count():
            adv.click(timeout=8000)
            page.wait_for_timeout(3000)
            out["advanced_expanded"] = True
    except Exception:
        out["advanced_expanded"] = False

    # ---- 4. 生成 Vibe CLI Key ----
    # 先直接调接口（页面已挂载且 ToS 已接受时可直接成功）
    out["vibe_cli_key"] = ""
    try:
        r = page.evaluate("""async () => {
            const res = await fetch('/api/code-trpc/apiKey.generateApiKey?batch=1',{
                method:'POST', credentials:'include',
                headers:{'Content-Type':'application/json','x-trpc-source':'nextjs-react'},
                body: JSON.stringify({"0":{json:null,meta:{values:["undefined"],v:1}}})});
            return JSON.stringify({s:res.status, b:(await res.text()).slice(0,400)});
        }""")
        parsed = json.loads(r)
        out["vibe_call_status"] = parsed.get("s")
        if parsed.get("s") == 200:
            d = json.loads(parsed["b"])
            if isinstance(d, list):
                j = d[0]["result"]["data"]["json"]
                out["vibe_cli_key"] = j.get("vibeApiKey", "")
                out["vibe_cli_key_id"] = j.get("vibeApiKeyId", "")
    except Exception as e:
        out["vibe_call_error"] = str(e)[:100]

    # 接口失败再走 UI 点击
    if not out["vibe_cli_key"]:
        try:
            gen = page.get_by_role("button", name="Generate API Key").first
            if gen.count():
                gen.click(timeout=10000)
                page.wait_for_timeout(10000)
                out["generate_clicked"] = True
                import re as _re
                found = _re.findall(r"mstrl_[A-Za-z0-9_]{20,}",
                                    page.locator("body").inner_text())
                if found:
                    out["vibe_cli_key"] = found[0]
        except Exception as e:
            out["generate_error"] = str(e)[:100]

    return out


# ---------------------------------------------------------------- 主流程
@dataclass
class HybridResult:
    email: str = ""
    password: str = ""
    sid: str = ""
    identity_id: str = ""
    org_uuid: str = ""
    workspace_uuid: str = ""
    keys: dict = field(default_factory=dict)
    vibe_cli_key: str = ""
    tos: dict = field(default_factory=dict)
    billing: dict = field(default_factory=dict)
    status: str = "pending"
    error: str = ""

    def to_dict(self):
        return self.__dict__.copy()


class HybridRunner:
    def __init__(self, pool: ProxyPool | None = None,
                 gw: GatewayPool | None = None, headless: bool = True):
        self.pool = pool or ProxyPool(DEFAULT_SIDS)
        self.gw = gw or GatewayPool()
        self.headless = headless
        self._log = lambda *a, **k: None

    def debug(self, on=True):
        if on:
            self._log = print
        return self

    # ---------- 入口 ----------
    def run(self, email: str, mailbox_url: str, password: str,
            first: str = "", last: str = "", region: str = "CA",
            skip_register: bool = False) -> HybridResult:
        r = HybridResult(email=email, password=password)
        try:
            px, local = find_working_proxy(self.pool, self.gw,
                                           "https://v2.auth.mistral.ai/login")
            if not px:
                raise RuntimeError("no working proxy")
            r.sid = px.sid
            prof = build_profile(email, region)
            r.billing = billing_address(email, region)

            # ========== 阶段 A: 注册 + 验证（协议） ==========
            if not skip_register:
                with SignupBrowser(prof, local, BrowserOptions(headless=self.headless)) as sb:
                    sb.page.goto("https://v2.auth.mistral.ai/login",
                                 wait_until="domcontentloaded", timeout=60000)
                    cookies = {c["name"]: c["value"] for c in sb.context.cookies()}

                sess = ProtocolSession(cookies=cookies, headers=prof.headers(), proxy=local)
                cli = ProtocolClient(sess)
                k = KratosProtocol(cli)

                reg = k.register(email, password, first, last)
                r.identity_id = reg["identity_id"]
                self._log(f"[A] registered identity={r.identity_id}")

                flow = k.start_verification(email)
                self._log("[A] code sent")

                code = self._wait_code(mailbox_url, email)
                if not code:
                    raise RuntimeError("no verification code")
                self._log(f"[A] code={code}")

                ver = k.submit_code(flow, code)
                if not ver.get("passed"):
                    raise RuntimeError(f"verify failed: {ver}")
                self._log("[A] verified")

            # ========== 阶段 B: 登录 + 协议业务 ==========
            with SignupBrowser(prof, local, BrowserOptions(headless=self.headless)) as sb:
                page = sb.page
                self._login(page, email, password, sb)
                cookies = {c["name"]: c["value"] for c in sb.context.cookies()}

                # --- 浏览器侧：console / chat ---
                r.tos["studio"] = browser_accept_studio_tos(page)
                self._log(f"[B] studio tos {r.tos['studio']}")
                chat_res = browser_chat_actions(page)
                r.tos["chat"] = chat_res.get("tos_call")
                r.vibe_cli_key = chat_res.get("vibe_cli_key", "")
                self._log(f"[B] chat actions {list(chat_res.keys())}")

            # --- 协议侧：admin 域全部 ---
            sess = ProtocolSession(cookies=cookies, headers=prof.headers(), proxy=local)
            cli = ProtocolClient(sess)
            adm = AdminProtocol(cli)

            me = adm.me_session()
            r.org_uuid = (me.get("organization") or {}).get("uuid", "")
            r.workspace_uuid = (me.get("workspace") or {}).get("uuid", "")
            self._log(f"[C] org={r.org_uuid} ws={r.workspace_uuid}")

            if r.workspace_uuid:
                for prod in ("API", "Vibe", "Studio"):
                    kr = adm.create_api_key(r.workspace_uuid, product=prod)
                    r.keys[prod] = kr.get("key", "")
                    self._log(f"[C] {prod}: {r.keys[prod][:20]}...")

            r.status = "ok"
        except Exception as e:
            r.status = "failed"
            r.error = f"{type(e).__name__}: {e}"
            self._log(f"[FAIL] {r.error}")
        return r

    # ---------- 内部 ----------
    def _login(self, page, email: str, password: str, sb):
        page.goto("https://v2.auth.mistral.ai/login",
                  wait_until="domcontentloaded", timeout=60000)
        page.wait_for_selector("input[name='email']", timeout=30000)
        sb.human_pause(1.5, 2.5)
        sb.human_type(page, "input[name='email']", email)
        page.get_by_role("button", name="Continue").first.click(timeout=10000)
        page.wait_for_selector("input[name='password']", timeout=20000)
        sb.human_pause(0.6, 1.2)
        sb.human_type(page, "input[name='password']", password)
        sb.human_pause(0.4, 1.0)
        page.get_by_role("button", name="Continue with password").first.click(timeout=10000)
        page.wait_for_timeout(10000)

    def _wait_code(self, mailbox_url: str, email: str,
                   timeout_s: int = 240) -> Optional[str]:
        mbx = ICloudMailbox(Mailbox.from_line(f"{email}----{mailbox_url}"),
                            proxy="socks5h://127.0.0.1:10808")
        base = set()
        j = mbx.fetch("all")
        if j.get("ok"):
            base = {e.get("id") for e in (j.get("data") or {}).get("emails", [])}
        deadline = time.time() + timeout_s
        while time.time() < deadline:
            j = mbx.fetch("all")
            if j.get("ok"):
                for e in (j.get("data") or {}).get("emails", []):
                    if e.get("id") in base:
                        continue
                    blob = ((e.get("fromAddress") or "") + (e.get("subject") or "")).lower()
                    if "mistral" not in blob:
                        continue
                    m = re.search(r"\b(\d{6})\b", e.get("textContent") or "")
                    if m:
                        return m.group(1)
            time.sleep(5)
        return None
