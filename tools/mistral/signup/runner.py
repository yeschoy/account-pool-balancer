"""
Mistral 注册机主流程

阶段:
  1. prepare     分配代理（含健康检查）、派生指纹、生成密码
  2. register    浏览器走 Kratos 注册（邮箱 -> Signup 表单）
  3. verify      从 icloud.mida.vip 取码并提交
  4. subscribe   admin 域订阅（可选：配卡）
  5. apikeys     生成 API / Vibe / Studio 三把 Key

关键约束（实测得出）:
  - 整个流程必须在【同一个浏览器 context】内完成
    跨 context 会丢失会话 cookie，后续全部 302 到登录页
  - 验证码每次"发送"都会作废旧码，必须只认发送之后到达的邮件
"""
from __future__ import annotations

import json
import random
import re
import string
import time
from dataclasses import asdict, dataclass, field
from pathlib import Path
from typing import Optional

from core.browser import BrowserOptions, SignupBrowser
from core.chain_gateway import GatewayPool, find_working_proxy
from core.fingerprint import DeviceProfile, billing_address, build_profile, resolve_country
from core.mailbox import ICloudMailbox, Mailbox
from core.proxy_pool import DEFAULT_SIDS, ProxyPool
from core.subscribe import ensure_studio_tos

AUTH = "https://auth.mistral.ai"
AUTH_UI = "https://v2.auth.mistral.ai"
ADMIN = "https://admin.mistral.ai"
RETURN_TO = f"{ADMIN}/subscriptions/upgrade/plans"


# ---------------------------------------------------------------- 账号模型
@dataclass
class Account:
    email: str
    mailbox_url: str
    password: str = ""
    region: str = "CA"
    platform: str = ""
    sid: str = ""
    proxy_url: str = ""
    first_name: str = ""
    last_name: str = ""

    identity_id: str = ""
    session_id: str = ""
    org_uuid: str = ""
    workspace_uuid: str = ""
    keys: dict = field(default_factory=dict)
    subscription: dict = field(default_factory=dict)
    billing: dict = field(default_factory=dict)
    storage_state: dict = field(default_factory=dict)
    status: str = "pending"
    error: str = ""
    created_at: str = ""
    # 实测出口 IP / 国家（cloudflare trace），画像与账单地址都按它来
    exit_ip: str = ""
    country: str = ""

    def to_dict(self) -> dict:
        d = asdict(self)
        d.pop("storage_state", None)   # 体积大，单独存
        return d


def gen_password(length: int = 14) -> str:
    lower, upper = string.ascii_lowercase, string.ascii_uppercase
    digits, symbols = string.digits, "!@#$%"
    pw = [random.choice(lower), random.choice(upper),
          random.choice(digits), random.choice(symbols)]
    pool = lower + upper + digits + symbols
    pw += [random.choice(pool) for _ in range(length - len(pw))]
    random.shuffle(pw)
    return "".join(pw)


def gen_name(seed: str) -> tuple[str, str]:
    """从邮箱派生稳定的西式姓名（与 en-CA / en-US 画像一致）"""
    import hashlib
    FIRST = ["James", "Michael", "Daniel", "Ryan", "Ethan", "Noah", "Liam",
             "Sarah", "Emily", "Olivia", "Emma", "Chloe", "Ava", "Mia",
             "Jack", "Owen", "Lucas", "Grace", "Ella", "Sophie"]
    LAST = ["Smith", "Johnson", "Brown", "Wilson", "Taylor", "Anderson",
            "Martin", "Thompson", "White", "Clark", "Lewis", "Walker",
            "Hall", "Young", "King", "Wright", "Scott", "Green", "Baker"]
    h = hashlib.sha256(seed.encode()).hexdigest()
    return FIRST[int(h[:8], 16) % len(FIRST)], LAST[int(h[8:16], 16) % len(LAST)]


# ---------------------------------------------------------------- 主流程
class SignupRunner:
    def __init__(self, proxy_pool: ProxyPool, gateway_pool: GatewayPool | None = None,
                 headless: bool = False, debug: bool = False):
        self.pool = proxy_pool
        self.gw = gateway_pool or GatewayPool(pre_proxy=__import__("os").environ.get("MIDA_PROXY") or None)
        self.headless = headless
        self.debug = debug
        self._local: Optional[str] = None
        self._log = print if debug else (lambda *a, **k: None)

    # ---------- 入口 ----------
    def run(self, acct: Account, do_subscribe: bool = False,
            card: dict | None = None) -> Account:
        try:
            self._prepare(acct)
            prof = self.profile_for(acct)
            opts = BrowserOptions(headless=self.headless)

            with SignupBrowser(prof, self._local, opts) as sb:
                self._register(acct, sb)
                self._verify(acct, sb)
                # AI Studio / Pro 的 ToS 必须接受，否则 Key 用不了
                ensure_studio_tos(sb.page, debug=False)
                if do_subscribe:
                    self._subscribe(acct, sb, card)
                self._create_keys(acct, sb)
                acct.storage_state = sb.context.storage_state()

            acct.status = "ok"
        except Exception as e:
            acct.status = "failed"
            acct.error = f"{type(e).__name__}: {e}"
            self._log(f"[FAIL] {acct.email}: {acct.error}")
        acct.created_at = acct.created_at or time.strftime("%Y-%m-%dT%H:%M:%S")
        return acct

    # ---------- 1. 准备 ----------
    def _prepare(self, acct: Account):
        acct.password = acct.password or gen_password()
        acct.first_name, acct.last_name = gen_name(acct.email)
        px, local, geo = find_working_proxy(self.pool, self.gw)
        if not px:
            raise RuntimeError("no working proxy passed health check")
        acct.sid = px.sid
        acct.proxy_url = px.url
        acct.exit_ip = (geo or {}).get("ip", "")
        acct.country = resolve_country((geo or {}).get("country"), acct.region)
        self._local = local
        acct.billing = billing_address(acct.email, acct.country)
        self._log(f"[prepare] {acct.email} sid={px.sid} exit={acct.exit_ip} "
                  f"country={acct.country} local={local}")

    def profile_for(self, acct: Account) -> DeviceProfile:
        return build_profile(acct.email, acct.country or acct.region,
                             acct.platform or None)

    def local_proxy(self, acct: Account) -> str:
        return self._local or self.gw.get(acct.proxy_url)

    # ---------- 2. 注册 ----------
    def _register(self, acct: Account, sb: SignupBrowser):
        page = sb.page
        page.goto(f"{AUTH_UI}/login", wait_until="domcontentloaded", timeout=60000)
        sb.human_pause(1.2, 2.8)

        # 步骤1: 邮箱
        page.wait_for_selector("input[name='email']", timeout=30000)
        sb.human_type(page, "input[name='email']", acct.email)
        sb.human_pause(0.4, 1.1)
        page.get_by_role("button", name="Continue").first.click(timeout=10000)

        # 步骤2: 等 Signup 表单（账号不存在时出现）
        page.wait_for_selector("input[name='password']", timeout=25000)
        sb.human_pause(0.5, 1.4)
        self._log(f"[register] signup form ready url={page.url}")

        # 步骤3: 填密码 + 姓名
        sb.human_type(page, "input[name='password']", acct.password)
        sb.human_pause(0.3, 0.8)
        sb.human_type(page, "input[name='firstName']", acct.first_name)
        sb.human_pause(0.2, 0.6)
        sb.human_type(page, "input[name='lastName']", acct.last_name)
        sb.human_pause(0.4, 1.0)

        page.get_by_role("button", name="Signup").first.click(timeout=10000)
        page.wait_for_timeout(8000)
        self._log(f"[register] after submit url={page.url}")

        body = page.locator("body").inner_text()[:600]
        if "already" in body.lower() or "exists" in body.lower():
            raise RuntimeError(f"account already exists: {body[:160]}")
        if "check your inbox" not in body.lower() and "verify" not in body.lower():
            raise RuntimeError(f"signup did not reach verification: {body[:200]}")

        acct.status = "registered"

    # ---------- 3. 验证码 ----------
    def _verify(self, acct: Account, sb: SignupBrowser):
        page = sb.page
        mbx = ICloudMailbox(Mailbox.from_line(f"{acct.email}----{acct.mailbox_url}"),
                            proxy=__import__("os").environ.get("MIDA_PROXY") or None)

        # 注册后应已在 /verification 页；不在就导航过去
        if "/verification" not in page.url:
            page.goto(f"{AUTH_UI}/verification", wait_until="domcontentloaded", timeout=60000)
            sb.human_pause(2.0, 3.0)
            sb.human_type(page, "input[name='email']", acct.email)
            sb.human_pause(0.5, 1.2)
            page.get_by_role("button", name="Send me a code by email").first.click(timeout=10000)
        else:
            # 已在验证页，若只有 email 框需要点发送
            if page.locator("input[inputmode='numeric']").count() == 0:
                sb.human_type(page, "input[name='email']", acct.email)
                sb.human_pause(0.5, 1.2)
                page.get_by_role("button", name="Send me a code by email").first.click(timeout=10000)

        page.wait_for_selector("input[inputmode='numeric']", timeout=25000)
        sent_at = time.time()
        self._log("[verify] code requested")

        code = self._wait_fresh_code(mbx, sent_at, timeout_s=240)
        if not code:
            raise RuntimeError("no fresh verification code within 240s")
        self._log(f"[verify] code={code}")

        sb.human_pause(0.6, 1.4)
        box = page.locator("input[inputmode='numeric']").first
        box.click()
        for ch in code:
            page.keyboard.type(ch, delay=random.randint(90, 180))
            time.sleep(random.uniform(0.06, 0.14))

        page.wait_for_timeout(9000)
        body = page.locator("body").inner_text()[:400]
        self._log(f"[verify] result={body[:120]}")
        if "successfully verified" not in body.lower():
            raise RuntimeError(f"verification failed: {body[:200]}")

        acct.status = "verified"

    @staticmethod
    def _wait_fresh_code(mbx: ICloudMailbox, since_ts: float,
                         timeout_s: int = 240, interval_s: int = 5) -> Optional[str]:
        """取基线 id 集合，只认之后新到达的 Mistral 邮件"""
        baseline: set = set()
        j = mbx.fetch("all")
        if j.get("ok"):
            baseline = {e.get("id") for e in (j.get("data") or {}).get("emails", [])}

        deadline = time.time() + timeout_s
        while time.time() < deadline:
            j = mbx.fetch("all")
            if j.get("ok"):
                for e in (j.get("data") or {}).get("emails", []):
                    if e.get("id") in baseline:
                        continue
                    blob = (e.get("fromAddress") or "") + (e.get("subject") or "")
                    if "mistral" not in blob.lower():
                        continue
                    m = re.search(r"\b(\d{6})\b", e.get("textContent") or "")
                    if m:
                        return m.group(1)
            time.sleep(interval_s)
        return None

    # ---------- 4. 订阅 ----------
    def _subscribe(self, acct: Account, sb: SignupBrowser, card: dict | None):
        page = sb.page
        page.goto(f"{ADMIN}/subscription/upgrade/subscribe?plan=INDIVIDUAL&periodicity=MONTHLY",
                  wait_until="domcontentloaded", timeout=60000)
        sb.human_pause(2.5, 4.5)
        self._log(f"[subscribe] landed {page.url}")
        # 填卡逻辑见 subscribe.py
        acct.status = "subscribed"

    # ---------- 5. 生成 Key ----------
    def _create_keys(self, acct: Account, sb: SignupBrowser):
        page = sb.page
        page.goto(f"{ADMIN}/organization/api-keys", wait_until="domcontentloaded", timeout=60000)
        sb.human_pause(2.0, 3.5)

        ws = page.evaluate("""async () => {
            const u = new URLSearchParams({input: JSON.stringify({json:null,meta:{values:["undefined"],v:1}})});
            const r = await fetch('/api/local-trpc/user.meSession?'+u, {credentials:'include',
                headers:{'x-trpc-source':'nextjs-react'}});
            return await r.text();
        }""")
        try:
            d = json.loads(ws)["result"]["data"]["json"]
            acct.workspace_uuid = d["workspace"]["uuid"]
            acct.org_uuid = d["organization"]["uuid"]
            acct.identity_id = d.get("user", {}).get("uuid", "") if isinstance(d, dict) else ""
        except Exception as e:
            self._log(f"[keys] meSession parse fail: {e} / {ws[:150]}")

        if not acct.workspace_uuid:
            raise RuntimeError("workspace uuid not found (session may be lost)")

        for product in ("API", "Vibe", "Studio"):
            body = {
                "name": f"{product.lower()}-{random.randint(1000, 9999)}",
                "workspace_uuid": acct.workspace_uuid,
                "primitive_access_scope": "personal_and_shared",
                "product": product,
            }
            res = page.evaluate("""async (b) => {
                const r = await fetch('/api/billing/api-keys', {
                    method:'POST', credentials:'include',
                    headers:{'Content-Type':'application/json'},
                    body: JSON.stringify(b)});
                return JSON.stringify({s:r.status, b: await r.text()});
            }""", body)
            try:
                j = json.loads(res)
                if j["s"] == 200:
                    k = json.loads(j["b"])
                    acct.keys[product] = k.get("key", "")
                    self._log(f"[keys] {product}: {k.get('key','')[:24]}...")
                else:
                    self._log(f"[keys] {product} FAIL {j['s']}: {j['b'][:150]}")
            except Exception as e:
                self._log(f"[keys] {product} parse fail: {e}")

        acct.status = "complete"


# ---------------------------------------------------------------- 持久化
class Store:
    def __init__(self, path: str):
        self.path = Path(path)
        self.path.parent.mkdir(parents=True, exist_ok=True)
        self.rows: list[dict] = []
        if self.path.exists():
            try:
                self.rows = json.loads(self.path.read_text())
            except Exception:
                self.rows = []

    def upsert(self, acct: Account):
        d = acct.to_dict()
        for i, r in enumerate(self.rows):
            if r.get("email") == d["email"]:
                self.rows[i] = d
                break
        else:
            self.rows.append(d)
        self.path.write_text(json.dumps(self.rows, indent=1, ensure_ascii=False))

    def done_emails(self) -> set[str]:
        return {r["email"] for r in self.rows if r.get("status") in ("ok", "complete")}
