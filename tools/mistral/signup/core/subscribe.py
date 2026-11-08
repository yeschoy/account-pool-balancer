"""
订阅流程（配卡）

页面: /subscription/upgrade/subscribe?plan=INDIVIDUAL&periodicity=MONTHLY

实测结构（2026-11-07）:
  Step A  计费信息表单
     radio: Individual / Company
     input: legal_name, email, tax_identification_number(可选),
            address_line1, city, zipcode, 省份下拉, 国家
     按钮: Continue
  Step B  Payment methods -> Stripe Elements
     Stripe hosted field iframe (js.stripe.com) 内填卡
     按钮: Continue / Subscribe

接口层（已实测，可用于无 UI 复刻）:
  POST /api/local-trpc/billing.updateInfo
       {"json":{"legal_name","email","address_line1","city","zipcode",
                "country","state","customer_type","currency"}}
  POST /api/local-trpc/billing.createPaymentMethodSetupIntent
       -> {"intent_secret": "seti_..._secret_..."}
  POST /api/local-trpc/billing.subscribeLeChat
       {"json":{"plan":"INDIVIDUAL","periodicity":"MONTHLY","seats":null}}

注意:
  - 卡信息由 Stripe iframe 采集，不经过 mistral 域名
  - 卡号绝不可跨账号复用（Stripe 会做卡片-账号聚类）
"""
from __future__ import annotations

import json
import random
import re
import time
from typing import Optional

from core.browser import SignupBrowser

ADMIN = "https://admin.mistral.ai"


def luhn_ok(card: str) -> bool:
    digits = [int(d) for d in re.sub(r"\D", "", card)]
    s = 0
    for i, d in enumerate(reversed(digits)):
        if i % 2:
            d *= 2
            if d > 9:
                d -= 9
        s += d
    return s % 10 == 0


def detect_brand(card: str) -> str:
    n = re.sub(r"\D", "", card)
    if n.startswith("4"):
        return "visa"
    if re.match(r"^5[1-5]", n) or re.match(r"^2[2-7]", n):
        return "mastercard"
    if re.match(r"^3[47]", n):
        return "amex"
    if n.startswith("6"):
        return "discover"
    return "unknown"


class Subscriber:
    def __init__(self, sb: SignupBrowser, debug: bool = False):
        self.sb = sb
        self.page = sb.page
        self._log = print if debug else (lambda *a, **k: None)

    # ---------- 主流程 ----------
    def subscribe(self, card: dict, billing: dict,
                  plan: str = "INDIVIDUAL", periodicity: str = "MONTHLY",
                  customer_type: str = "individual") -> dict:
        """
        card:      {"number","exp_month","exp_year","cvc","name"}
        billing:   {"legal_name","email","address_line1","city","zipcode","state","country"}
        """
        if not luhn_ok(card["number"]):
            raise ValueError("card number fails Luhn check")

        self.page.goto(
            f"{ADMIN}/subscription/upgrade/subscribe?plan={plan}&periodicity={periodicity}",
            wait_until="domcontentloaded", timeout=60000)
        self.sb.human_pause(2.5, 4.5)
        self._log(f"[sub] landed {self.page.url}")

        self._fill_billing(billing, customer_type)
        self._fill_card(card)
        return self._finalize()

    # ---------- Step A: 计费信息 ----------
    def _fill_billing(self, b: dict, customer_type: str = "individual"):
        page = self.page
        sb = self.sb

        # 选 Individual / Company
        try:
            label = "Individual" if customer_type.lower().startswith("i") else "Company"
            page.get_by_text(label, exact=True).first.click(timeout=6000)
            sb.human_pause(0.4, 1.0)
        except Exception as e:
            self._log(f"[sub] customer_type select failed: {e}")

        fields = {
            "input[name='legal_name']": b.get("legal_name", ""),
            "input[name='email']": b.get("email", ""),
            "input[name='address_line1']": b.get("address_line1", ""),
            "input[name='city']": b.get("city", ""),
            "input[name='zipcode']": b.get("zipcode", ""),
        }
        for sel, val in fields.items():
            if not val:
                continue
            try:
                if page.locator(sel).count():
                    self._set_input(sel, val)
                    sb.human_pause(0.2, 0.5)
                    self._log(f"[sub] filled {sel} -> {page.input_value(sel)!r}")
            except Exception as e:
                self._log(f"[sub] fill {sel} failed: {e}")

        # 省份：实测是 role=combobox，aria-label="Province or territory"
        state = b.get("state") or b.get("state_code")
        if state:
            picked = False
            try:
                cb = page.get_by_role("combobox", name=re.compile("Province|State", re.I)).first
                if cb.count():
                    cb.click(timeout=8000)
                    sb.human_pause(0.6, 1.3)
                    try:
                        opt = page.get_by_role("option", name=re.compile(f"^{re.escape(state)}$", re.I)).first
                        if opt.count():
                            opt.click(timeout=6000)
                            picked = True
                    except Exception:
                        pass
                    if not picked:
                        page.keyboard.type(state[:4], delay=80)
                        sb.human_pause(0.3, 0.7)
                        page.keyboard.press("Enter")
                        picked = True
            except Exception as e:
                self._log(f"[sub] province combobox failed: {e}")
            if not picked:
                for sel in ("select[name='state']", "select[name='province']"):
                    if page.locator(sel).count():
                        try:
                            page.select_option(sel, label=state)
                            picked = True
                            break
                        except Exception:
                            pass
            self._log(f"[sub] province picked={picked} ({state})")

        sb.human_pause(0.6, 1.6)
        try:
            page.get_by_role("button", name="Continue").first.click(timeout=10000)
            page.wait_for_timeout(5000)
            self._log(f"[sub] after continue url={page.url}")
        except Exception as e:
            self._log(f"[sub] continue failed: {e}")

    def _set_input(self, selector: str, value: str):
        """
        用原生 setter 写入，确保 React 受控组件感知到变化。
        直接 fill() 有时不会触发 React 的 onChange，导致校验仍报"请填写"。
        """
        ok = self.page.evaluate("""([sel, val]) => {
            const el = document.querySelector(sel);
            if (!el) return false;
            const setter = Object.getOwnPropertyDescriptor(
                window.HTMLInputElement.prototype, 'value').set;
            setter.call(el, val);
            el.dispatchEvent(new Event('input', { bubbles: true }));
            el.dispatchEvent(new Event('change', { bubbles: true }));
            el.dispatchEvent(new Event('blur', { bubbles: true }));
            return true;
        }""", [selector, value])
        if not ok:
            raise RuntimeError(f"element not found: {selector}")

    # ---------- Step B: Stripe 卡 ----------
    # 实测（2026-11-07）:
    #   iframe title="Secure payment input frame"
    #   内部字段: number(cc-number) / expiry(cc-exp) / cvc(cc-csc) / postalCode(billing postal-code)
    def _fill_card(self, card: dict, postal: str = "") -> bool:
        page = self.page
        sb = self.sb

        # 等支付 iframe 挂载
        deadline = time.time() + 45
        frame_sel = "iframe[title='Secure payment input frame']"
        while time.time() < deadline:
            if page.locator(frame_sel).count():
                break
            page.wait_for_timeout(1000)
        else:
            self._log("[sub] payment iframe not found")
            return False

        fr = page.frame_locator(frame_sel)

        # 卡号
        try:
            fr.locator("input[autocomplete='cc-number']").fill("")
            fr.locator("input[autocomplete='cc-number']").type(card["number"], delay=random.randint(30, 90))
            sb.human_pause(0.3, 0.8)
        except Exception as e:
            self._log(f"[sub] card number fill failed: {e}")

        # 有效期
        exp = f"{int(card['exp_month']):02d} / {str(card['exp_year'])[-2:]}"
        try:
            fr.locator("input[autocomplete='cc-exp']").fill("")
            fr.locator("input[autocomplete='cc-exp']").type(exp, delay=random.randint(40, 110))
            sb.human_pause(0.2, 0.6)
        except Exception as e:
            self._log(f"[sub] expiry fill failed: {e}")

        # CVC
        try:
            fr.locator("input[autocomplete='cc-csc']").fill("")
            fr.locator("input[autocomplete='cc-csc']").type(str(card["cvc"]), delay=random.randint(50, 130))
            sb.human_pause(0.2, 0.6)
        except Exception as e:
            self._log(f"[sub] cvc fill failed: {e}")

        # 邮编（Stripe 可能要求）
        if postal:
            try:
                loc = fr.locator("input[autocomplete='billing postal-code']")
                if loc.count():
                    loc.fill("")
                    loc.type(postal, delay=60)
            except Exception as e:
                self._log(f"[sub] postal fill skipped: {e}")

        # 卡主姓名（新版 Stripe Link 字段）
        if card.get("name"):
            try:
                loc = fr.locator("input[autocomplete='billing name']")
                if loc.count():
                    loc.fill(card["name"])
            except Exception:
                pass

        sb.human_pause(1.0, 2.0)
        self._log("[sub] card filled")
        return True

    # ---------- 提交 ----------
    def _finalize(self) -> dict:
        page = self.page
        sb = self.sb

        # 勾选条款
        for sel in ("input[type='checkbox']",):
            try:
                cbs = page.locator(sel)
                for i in range(cbs.count()):
                    cb = cbs.nth(i)
                    if not cb.is_checked():
                        cb.check(timeout=3000)
                        sb.human_pause(0.2, 0.5)
            except Exception:
                pass

        result = {}
        for name in ("Subscribe", "Continue", "Confirm"):
            try:
                btn = page.get_by_role("button", name=name).first
                if btn.count():
                    btn.click(timeout=8000)
                    page.wait_for_timeout(8000)
                    self._log(f"[sub] clicked {name}, url={page.url}")
                    result["clicked"] = name
                    break
            except Exception:
                continue

        # 读结果
        body = page.locator("body").inner_text()[:800]
        result["url"] = page.url
        result["body"] = body[:400]

        # 通过接口确认订阅状态
        try:
            sub = page.evaluate("""async () => {
                const u = new URLSearchParams({input: JSON.stringify({json:{planType:"API"}})});
                const r = await fetch('/api/local-trpc/billing.subscriptions.get?'+u,
                  {credentials:'include', headers:{'x-trpc-source':'nextjs-react'}});
                return await r.text();
            }""")
            result["subscription_raw"] = sub[:500]
        except Exception as e:
            result["subscription_raw"] = f"err: {e}"

        return result


# ---------------------------------------------------------------- 取消订阅
def cancel_subscription(page, plan_type: str = "API") -> dict:
    """
    实测唯一入口（tRPC）：
        POST /api/local-trpc/billing.subscriptions.cancelUnsubscription
        {"json":{"planType":"API"}}
    返回 400 "No subscription in cancellation" 表示本来就没有订阅。
    """
    res = page.evaluate("""async (planType) => {
        const r = await fetch('/api/local-trpc/billing.subscriptions.cancelUnsubscription', {
            method:'POST', credentials:'include',
            headers:{'Content-Type':'application/json','x-trpc-source':'nextjs-react'},
            body: JSON.stringify({json:{planType}})});
        return JSON.stringify({s:r.status, b:await r.text()});
    }""", plan_type)
    try:
        j = json.loads(res)
    except Exception:
        return {"ok": False, "raw": res}
    if j["s"] == 200:
        return {"ok": True, "body": json.loads(j["b"])}
    return {"ok": False, "status": j["s"], "body": j["b"][:300]}


# ---------------------------------------------------------------- ToS 接受
CONSOLE = "https://console.mistral.ai"


def accept_studio_tos(page, debug: bool = False) -> dict:
    """
    接受 AI Studio / Pro 的 Terms of Service（弹窗 "Accept and continue"）。

    实测接口（2026-11-07）:
        POST https://console.mistral.ai/api-ui/trpc/user.acceptTos?batch=1
        x-trpc-source: nextjs-react
        trpc-accept: application/jsonl
        content-type: application/json
        x-csrftoken: <从 csrftoken cookie 读>
        referer: https://console.mistral.ai/plateforme/studio
        {"0":{"json":null,"meta":{"values":["undefined"],"v":1}}}

    注意:
      - 域名是 console.mistral.ai（不是 admin）
      - 路径前缀是 /api-ui/trpc/（不是 /api/trpc/ 或 /api/local-trpc/）
      - 必须 ?batch=1 + batch 对象 body
      - 缺 x-csrftoken 会 403
      - 幂等：已接受则弹窗不再出现
    """
    # 先把 csrftoken 从 cookie 里取出来
    csrf = None
    try:
        for c in page.context.cookies():
            if c["name"] == "csrftoken" and "console.mistral.ai" in c.get("domain", ""):
                csrf = c["value"]
                break
    except Exception:
        pass

    if not csrf:
        # 兜底：从 document.cookie 读
        try:
            csrf = page.evaluate("""() => {
                const m = document.cookie.match(/(?:^|; )csrftoken=([^;]+)/);
                return m ? decodeURIComponent(m[1]) : null;
            }""")
        except Exception:
            pass

    res = page.evaluate("""async (csrf) => {
        const headers = {
            'Content-Type': 'application/json',
            'x-trpc-source': 'nextjs-react',
            'trpc-accept': 'application/jsonl',
        };
        if (csrf) headers['x-csrftoken'] = csrf;
        const r = await fetch('/api-ui/trpc/user.acceptTos?batch=1', {
            method: 'POST', credentials: 'include',
            headers,
            body: JSON.stringify({"0": {json: null, meta: {values: ["undefined"], v: 1}}}),
        });
        return JSON.stringify({s: r.status, b: (await r.text()).slice(0, 300)});
    }""", csrf)

    out = {"ok": False, "csrf": bool(csrf), "raw": res}
    try:
        j = json.loads(res)
        out["status"] = j["s"]
        out["ok"] = j["s"] == 200
        out["body"] = j["b"]
    except Exception:
        pass
    if debug:
        print(f"[tos] {out}")
    return out


def ensure_studio_tos(page, debug: bool = False) -> bool:
    """
    打开 Studio，如果出现 ToS 弹窗就接受。
    返回是否已通过（无论原本就接受过还是刚接受）。
    """
    page.goto(f"{CONSOLE}/plateforme/studio", wait_until="domcontentloaded", timeout=60000)
    page.wait_for_timeout(6000)

    body = page.locator("body").inner_text()
    if "Accept and continue" not in body and "Terms of Service" not in body:
        if debug:
            print("[tos] no dialog (already accepted or page unavailable)")
        return True

    # 优先点真实按钮（带上正常 referer / 指纹），失败再走接口
    try:
        btn = page.get_by_role("button", name="Accept and continue").first
        if btn.count():
            btn.click(timeout=10000)
            page.wait_for_timeout(6000)
            if debug:
                print("[tos] clicked Accept and continue")
            return True
    except Exception as e:
        if debug:
            print(f"[tos] button click failed: {e}")

    r = accept_studio_tos(page, debug=debug)
    return r.get("ok", False)
