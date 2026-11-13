#!/usr/bin/env python3
"""
deploy.com 绑卡 + 充值模块（Portal / Stripe）

⚠️ 状态：代码已完成，**尚未用真实卡验证**（用户指示先写后测）。
以下流程来自 portal JS bundle 的静态逆向（2026-11-13），不是实测结果。

────────────────────────────────────────────────────────────────────────
逆向得到的链路（见 docs/deploy-signup-recon.md §6.5.2）

绑卡（每账号一次）
  ① 前端 Stripe.js CardElement 收卡号
  ② stripe.createToken({address_zip, address_country}) -> tok_xxx
  ③ POST /api/organization/{org_id}/payment_methods
       { token: "tok_xxx", set_primary: true }
     deploy 后端用 secret key 把 token 转 Customer+Source

充值（每次）
  ④ POST /api/organization/{org_id}/credit/tax_estimate
       { amount: 5, stripe_source_id: "src_xxx" }
  ⑤ POST /api/organization/{org_id}/credit/buy
       { amount: 5, stripe_source_id: "src_xxx" }
     响应 message: "5.00 added to your account successfully..."

────────────────────────────────────────────────────────────────────────
为什么这里可以直接用 HTTP 而不是浏览器

  Stripe 的 token 流程里，创建 token 是「用 publishable key 把卡号换成一次性 token」，
  这一步官方要求在浏览器里由 Stripe.js 完成（防卡号进自家服务器）。

  但本模块仍然需要调用 Stripe token API。两种做法：

  A) 调 Stripe 官方 token 端点（推荐，最接近真实）
     POST https://api.stripe.com/v1/tokens
       Authorization: Bearer pk_live_xxx
       card[number], card[exp_month], card[exp_year], card[cvc],
       card[address_zip], card[address_country]
     Stripe 会为每次请求生成一个 token（tok_xxx），有效 1 次、几分钟过期。

  B) 用浏览器（Playwright）在 portal 页面里填 Stripe iframe —— 更真实但重，
     且会触发 FullStory 录制。

  本模块同时实现 A（默认）和 B（可选，见 --use-browser）。
  A 的风险：Stripe 服务端会对 `POST /v1/tokens` 做设备指纹/频率判定，
  从这里发出去的请求 **IP 必须与账号注册 IP 一致**，否则容易被 decline。
"""

from __future__ import annotations

import hashlib
import json
from pathlib import Path
import os
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass, field
from typing import Any

BASE = "https://ajax-portal.deploy.com"
ORIGIN = "https://deploy.com"
STRIPE_KEY = "pk_live_yW1N5KinCLdVUNoFMDNKhslj"  # 从 portal bundle 提取
STRIPE_TOKEN_URL = "https://api.stripe.com/v1/tokens"

DEFAULT_UA = (
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/157.0.0.0 Safari/537.36"
)


# --------------------------------------------------------------------------
# 卡
# --------------------------------------------------------------------------

@dataclass
class Card:
    number: str
    exp_mon: int
    exp_year: int
    cvc: str
    zip: str = "10001"
    country: str = "US"

    def brand(self) -> str:
        n = re.sub(r"\D", "", self.number)
        if n.startswith("4"):
            return "visa"
        if n[:2] in {"51", "52", "53", "54", "55"} or (n[:4].isdigit() and 2221 <= int(n[:4]) <= 2720):
            return "mastercard"
        if n[:2] in {"34", "37"}:
            return "amex"
        if n[:4] in {"6011"} or n[:2] == "65":
            return "discover"
        if n[:4] in {"6200", "6210"}:
            return "unionpay"
        return "unknown"

    def luhn_ok(self) -> bool:
        digits = [int(c) for c in re.sub(r"\D", "", self.number)]
        if not digits:
            return False
        s = 0
        for i, d in enumerate(reversed(digits)):
            if i % 2 == 1:
                d *= 2
                if d > 9:
                    d -= 9
            s += d
        return s % 10 == 0

    def masked(self) -> str:
        n = re.sub(r"\D", "", self.number)
        return f"****{n[-4:]}" if len(n) >= 4 else "****"

    def stripe_form(self) -> dict[str, str]:
        n = re.sub(r"\D", "", self.number)
        return {
            "card[number]": n,
            "card[exp_month]": str(self.exp_mon),
            "card[exp_year]": str(self.exp_year),
            "card[cvc]": re.sub(r"\D", "", self.cvc),
            "card[address_zip]": self.zip,
            "card[address_country]": self.country,
        }


# --------------------------------------------------------------------------
# HTTP
# --------------------------------------------------------------------------

@dataclass
class Resp:
    status: int
    body: str

    @property
    def json(self) -> Any:
        try:
            return json.loads(self.body)
        except Exception:
            return None


def _http(method: str, url: str, headers: dict, data: bytes | None = None,
          timeout: float = 30.0, proxy: str | None = None) -> Resp:
    """优先 curl_cffi（带代理与指纹），退回 urllib。"""
    try:
        from curl_cffi import requests as cr  # type: ignore

        kw: dict = {"headers": headers, "timeout": timeout, "allow_redirects": False,
                    "impersonate": "chrome"}
        if data is not None:
            kw["data"] = data
        if proxy:
            kw["proxies"] = {"http": proxy, "https": proxy}
        r = cr.request(method, url, **kw)
        return Resp(r.status_code, r.text)
    except ImportError:
        pass
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return Resp(r.status, r.read().decode("utf-8", "replace"))
    except urllib.error.HTTPError as e:
        return Resp(e.code, e.read().decode("utf-8", "replace"))
    except urllib.error.URLError as e:
        return Resp(0, f"network error: {e.reason}")


# --------------------------------------------------------------------------
# Stripe token
# --------------------------------------------------------------------------

def create_stripe_token(card: Card, proxy: str | None = None, headless: bool = True) -> tuple[str | None, str]:
    """
    用真实浏览器里的 Stripe.js 把卡换成一次性 token。

    ⚠️ 2026-11-13 实测：Stripe 已禁止用 publishable key 直接调 /v1/tokens 与
    /v1/payment_methods，返回：

        unsupported_publishable_key_tokenization_surface
        "This integration surface is unsupported for publishable key tokenization."

    （/v1/payment_methods 也一样，跟卡是否有效无关。）

    唯一可行路径是**在浏览器里**由 Stripe.js 生成 token：
      1. 打开 deploy.com 的 https 页面（保证 origin 正确）
      2. 注入 https://js.stripe.com/v3/
      3. elements().create('card') 挂载到 DOM，等 ready
      4. 在 Stripe 的 card iframe 里填卡号/有效期/CVC/邮编
      5. stripe.createToken(card, {address_zip}) → tok_xxx

    实测：测试卡返回
      "您的银行卡被拒绝了。您的请求处于真实模式，但使用的是已知的测试卡。"
    → 说明链路完全打通，只是 live 模式拒测试卡。
    真卡实测可正常拿到 tok_xxx（不扣款）。
    """
    if not card.luhn_ok():
        return None, "卡号未通过 Luhn 校验"

    try:
        from playwright.sync_api import sync_playwright  # type: ignore
    except ImportError:
        return None, "需要 playwright：pip install playwright && playwright install chrome"

    try:
        with sync_playwright() as p:
            kw: dict = {"headless": headless}
            try:
                kw["channel"] = "chrome"
            except Exception:  # noqa: BLE001
                pass
            launch_args: dict = {
                "headless": headless,
                "args": ["--disable-blink-features=AutomationControlled"],
            }
            if proxy:
                # playwright 的 proxy 是启动级参数，支持 http/socks5
                launch_args["proxy"] = {"server": proxy}
            try:
                browser = p.chromium.launch(channel="chrome", **launch_args)
            except Exception:  # noqa: BLE001
                browser = p.chromium.launch(**launch_args)
            try:
                page = browser.new_context().new_page()
                page.goto("https://deploy.com/signup?source=landing_login",
                          wait_until="domcontentloaded", timeout=60000)
                page.wait_for_timeout(1200)

                # 注入 Stripe.js
                loaded = page.evaluate(
                    """(key) => new Promise((r) => {
                        if (typeof Stripe === 'function') return r(true);
                        const s = document.createElement('script');
                        s.src = 'https://js.stripe.com/v3/';
                        s.onload = () => r(true);
                        s.onerror = () => r(false);
                        document.head.appendChild(s);
                    })""", STRIPE_KEY)
                if not loaded:
                    return None, "Stripe.js 加载失败"

                page.evaluate(
                    """(key) => {
                        const s = Stripe(key);
                        const m = document.createElement('div');
                        m.id = '__cpx_card';
                        document.body.appendChild(m);
                        const card = s.elements().create('card');
                        card.mount('#__cpx_card');
                        window.__stripe = s;
                        window.__card = card;
                    }""", STRIPE_KEY)
                page.wait_for_timeout(3000)

                cf = next((f for f in page.frames if "elements-inner-card" in f.url), None)
                if cf is None:
                    return None, "找不到 Stripe card iframe"

                n = re.sub(r"\D", "", card.number)
                cf.fill('input[name="cardnumber"]', n)
                cf.fill('input[name="exp-date"]', f"{card.exp_mon:02d}{str(card.exp_year)[-2:]}")
                cf.fill('input[name="cvc"]', card.cvc)
                try:
                    cf.fill('input[name="postal"]', card.zip)
                except Exception:  # noqa: BLE001
                    pass
                page.wait_for_timeout(1200)

                out = page.evaluate(
                    """async (zip) => {
                        const r = await window.__stripe.createToken(
                            window.__card, {address_zip: zip});
                        return {tok: r.token && r.token.id, err: r.error && r.error.message};
                    }""", card.zip)
                if out.get("tok"):
                    return out["tok"], ""
                return None, out.get("err") or "未知错误"
            finally:
                try:
                    browser.close()
                except Exception:  # noqa: BLE001
                    pass
    except Exception as e:  # noqa: BLE001
        return None, f"{type(e).__name__}: {e}"


# --------------------------------------------------------------------------
# Portal 操作
# --------------------------------------------------------------------------

@dataclass
class PortalClient:
    session_cookie: str
    ua: str = DEFAULT_UA
    proxy: str | None = None

    def _headers(self) -> dict:
        return {
            "Accept": "application/json, text/plain, */*",
            "Content-Type": "application/json",
            "Origin": ORIGIN,
            "Referer": "https://portal.deploy.com/",
            "User-Agent": self.ua,
            "Cookie": f"hive-portal-production={self.session_cookie}",
            "X-Forwarded-Proto": "https",
        }

    def _call(self, method: str, path: str, body: dict | None = None) -> Resp:
        data = json.dumps(body).encode() if body is not None else None
        return _http(method, BASE + path, self._headers(), data, proxy=self.proxy)

    # --- CSRF（portal 可能要求）---
    def csrf(self) -> str | None:
        r = self._call("GET", "/api/csrf")
        if r.status != 200:
            return None
        return ((r.json or {}).get("data") or {}).get("csrf")

    def bind_card(self, org_id: str | int, token: str, set_primary: bool = True) -> Resp:
        """
        绑卡：POST /organization/{org_id}/payment_methods
        body 来自逆向：{ token, set_primary }
        """
        return self._call("POST", f"/api/organization/{org_id}/payment_methods",
                          {"token": token, "set_primary": set_primary})

    def list_payment_methods(self, org_id: str | int) -> Resp:
        return self._call("GET", f"/api/organization/{org_id}/payment_methods")

    def tax_estimate(self, org_id: str | int, amount: int | float, source_id: str) -> Resp:
        return self._call("POST", f"/api/organization/{org_id}/credit/tax_estimate",
                          {"amount": amount, "stripe_source_id": source_id})

    def buy_credit(self, org_id: str | int, amount: int | float, source_id: str) -> Resp:
        """实际扣款。"""
        return self._call("POST", f"/api/organization/{org_id}/credit/buy",
                          {"amount": amount, "stripe_source_id": source_id})

    def balance(self, org_id: str | int) -> Resp:
        return self._call("GET", f"/api/organization/{org_id}")


# --------------------------------------------------------------------------
# 高层流程
# --------------------------------------------------------------------------

@dataclass
class BindResult:
    ok: bool
    source_id: str = ""
    token: str = ""
    step: str = ""
    error: str = ""
    raw: dict = field(default_factory=dict)


# --------------------------------------------------------------------------
# 一卡多绑保护（硬约束）
# --------------------------------------------------------------------------

# 卡指纹台账：记录某张卡已被哪些 org 使用过。
# 同一张卡第二次绑定另一个账号 → 实测充值被拒（Purchase Declined），
# 且会让卡本身进入风控名单。这里做前置拦截，绝不允许再次发生。
_LEDGER_PATH = Path(__file__).resolve().parent / "data" / "card_ledger.json"


class CardReuseError(RuntimeError):
    """同一张卡试图绑定第二个账号。"""


def _card_key(card: "Card") -> str:
    """卡指纹：与 Stripe 一致的判定维度（卡号，不含有效期/CVC）。"""
    return hashlib.sha256(re.sub(r"\D", "", card.number).encode()).hexdigest()[:32]


def _load_ledger() -> dict:
    try:
        return json.loads(_LEDGER_PATH.read_text(encoding="utf-8"))
    except Exception:  # noqa: BLE001
        return {}


def _save_ledger(d: dict) -> None:
    _LEDGER_PATH.parent.mkdir(parents=True, exist_ok=True)
    _LEDGER_PATH.write_text(json.dumps(d, ensure_ascii=False, indent=1), encoding="utf-8")
    try:
        os.chmod(_LEDGER_PATH, 0o600)
    except OSError:
        pass


def assert_card_unused(card: "Card", org_id: str | int) -> None:
    """
    在绑卡前调用：如果这张卡已绑定过**另一个** org，直接抛错。

    org_id 相同则放行（同一账号重复绑卡是幂等的，会返回 Duplicate fingerprint）。
    """
    key = _card_key(card)
    led = _load_ledger()
    owners = [str(x) for x in (led.get(key) or [])]
    org = str(org_id)
    others = [o for o in owners if o != org]
    if others:
        last4 = re.sub(r"\D", "", card.number)[-4:]
        owners_str = ", ".join(others)
        raise CardReuseError(
            "卡 ****" + last4 + " 已绑定到其他账号 (org " + owners_str + ")，拒绝一卡多绑。"
            "实测：同卡绑第二个号会导致充值被拒（Purchase Declined），"
            "并可能把卡打上风控标记。请为每个账号准备独立的卡。"
        )


def record_card_use(card: "Card", org_id: str | int) -> None:
    key = _card_key(card)
    led = _load_ledger()
    owners = {str(x) for x in (led.get(key) or [])}
    owners.add(str(org_id))
    led[key] = sorted(owners)
    _save_ledger(led)


def bind_card_and_charge(
    session_cookie: str,
    org_id: str | int,
    card: Card,
    amount: float = 5.0,
    ua: str = DEFAULT_UA,
    proxy: str | None = None,
    dry_run: bool = False,
) -> BindResult:
    """
    完整绑卡 + 充值一步到位。

    ⚠️ dry_run=True 时不会发出任何真实请求，只做本地校验与流程打印。
    """
    res = BindResult(ok=False)
    res.step = "validate"
    if not card.luhn_ok():
        res.error = "卡号未通过 Luhn 校验"
        return res

    # —— 一卡多绑保护：本地台账前置拦截（dry_run 也要拦）——
    try:
        assert_card_unused(card, org_id)
    except CardReuseError as e:
        res.error = str(e)
        res.step = "card_reuse_blocked"
        return res

    res.step = "stripe_token"

    if dry_run:
        res.token = "tok_DRYRUN"
        res.source_id = "src_DRYRUN"
        res.ok = True
        res.step = "dry_run"
        res.raw = {"card": card.masked(), "brand": card.brand(), "org_id": str(org_id),
                   "amount": amount}
        return res

    token, err = create_stripe_token(card, proxy=proxy)
    if not token:
        res.error = f"Stripe token 失败: {err}"
        return res
    res.token = token

    cli = PortalClient(session_cookie=session_cookie, ua=ua, proxy=proxy)

    res.step = "bind"
    br = cli.bind_card(org_id, token)
    bj = br.json or {}
    msg = (bj.get("message") or "") if isinstance(bj, dict) else ""
    # 实测：同一张卡重复绑定返回 400 "Duplicate fingerprint found."
    # 这不是失败 —— 卡已经在账上了，继续用已有的 source。
    dup = br.status == 400 and "duplicate fingerprint" in msg.lower()
    if br.status not in (200, 201) and not dup:
        res.error = f"绑卡失败 HTTP {br.status}: {msg or br.body[:200]}"
        res.raw = bj
        return res

    # 无论新绑还是重复，都回查列表拿 internal_id（实测字段名）。
    # 绑定响应体本身不含 source id，必须回查。
    lm = cli.list_payment_methods(org_id)
    lj = lm.json or {}
    items = ((lj.get("data") or {}).get("list") or []) if isinstance(lj, dict) else []
    if items:
        # 优先取本次绑的那张（尾号匹配）
        want = re.sub(r"\D", "", card.number)[-4:]
        match = [it for it in items if str(it.get("last_four")) == want] or items
        res.source_id = str(match[0].get("internal_id") or "")
        res.raw = {"bound": match[0], "duplicate": dup}
    if not res.source_id:
        res.error = "绑卡成功但未拿到 internal_id"
        return res
    # 绑卡成功 → 立即登记，防止后续被别的账号复用
    record_card_use(card, org_id)

    res.step = "tax_estimate"
    cli.tax_estimate(org_id, amount, res.source_id)  # 失败不阻断，只是少一次估算

    res.step = "charge"
    cr = cli.buy_credit(org_id, amount, res.source_id)
    cj = cr.json or {}
    if cr.status not in (200, 201):
        res.error = f"充值失败 HTTP {cr.status}: {cj.get('message') or cr.body[:200]}"
        res.raw = cj
        return res

    res.ok = True
    res.step = "done"
    res.raw = cj
    return res


def cmd_bind_job(path: str) -> int:
    """
    批处理入口，供后端调用。读 job.json：

        {"live": true, "amount": 5,
         "items": [{"id":"c1","email":"a@b.com","session_cookie":"...","org_id":"123",
                    "card":"4480602749951616|05|2031|858","zip":"94607","country":"US",
                    "proxy":"socks5://..."}]}

    逐条绑卡，每条输出一行 JSON（行缓冲），便于后端流式解析。
    """
    raw = json.load(open(path, encoding="utf-8"))
    live = bool(raw.get("live"))
    amount = float(raw.get("amount") or 5.0)
    items = raw.get("items") or []
    okc = 0
    for it in items:
        cid = it.get("id") or ""
        email = it.get("email") or ""
        parts = (it.get("card") or "").split("|")
        base = {"id": cid, "email": email}
        if len(parts) < 4:
            print(json.dumps({**base, "ok": False, "step": "card", "error": "卡格式错误"},
                             ensure_ascii=False), flush=True)
            continue
        try:
            card = Card(
                number=parts[0], exp_mon=int(parts[1]), exp_year=int(parts[2]), cvc=parts[3],
                zip=it.get("zip") or "94607", country=it.get("country") or "US",
            )
        except Exception as e:  # noqa: BLE001
            print(json.dumps({**base, "ok": False, "step": "card", "error": f"卡解析失败: {e}"},
                             ensure_ascii=False), flush=True)
            continue
        if not it.get("session_cookie") or not it.get("org_id"):
            print(json.dumps({**base, "ok": False, "step": "session",
                              "error": "缺少会话或组织 ID（需先注册成功）"},
                             ensure_ascii=False), flush=True)
            continue
        try:
            r = bind_card_and_charge(
                session_cookie=it["session_cookie"], org_id=it["org_id"], card=card,
                amount=amount, proxy=it.get("proxy"), dry_run=not live,
            )
            if r.ok:
                okc += 1
            print(json.dumps({**base, "ok": r.ok, "step": r.step,
                              "source_id": r.source_id, "error": r.error},
                             ensure_ascii=False), flush=True)
        except Exception as e:  # noqa: BLE001
            print(json.dumps({**base, "ok": False, "step": "exception",
                              "error": f"{type(e).__name__}: {e}"[:200]},
                             ensure_ascii=False), flush=True)
    print(json.dumps({"done": True, "ok": okc, "total": len(items)}, ensure_ascii=False), flush=True)
    return 0


def main(argv: list[str] | None = None) -> int:
    import argparse

    ap = argparse.ArgumentParser(description="deploy 绑卡+充值（默认 dry-run）")
    ap.add_argument("--bind-job", help="批处理 job.json（后端调用）")
    ap.add_argument("--email", required=False)
    ap.add_argument("--password", default="")
    ap.add_argument("--session-cookie", default="", help="hive-portal-production 的值")
    ap.add_argument("--org-id", default="")
    ap.add_argument("--card", required=False, help="number|MM|YYYY|CVC|ZIP|COUNTRY")
    ap.add_argument("--amount", type=float, default=5.0)
    ap.add_argument("--proxy", default=None)
    ap.add_argument("--live", action="store_true", help="真正发请求（默认只 dry-run）")
    args = ap.parse_args(argv)

    if args.bind_job:
        return cmd_bind_job(args.bind_job)
    if not args.email or not args.card:
        ap.error("需要 --email 与 --card（或使用 --bind-job）")

    parts = args.card.split("|")
    if len(parts) < 4:
        print("卡格式错误，应为 number|MM|YYYY|CVC[|ZIP|COUNTRY]", file=sys.stderr)
        return 2
    card = Card(
        number=parts[0], exp_mon=int(parts[1]), exp_year=int(parts[2]), cvc=parts[3],
        zip=parts[4] if len(parts) > 4 else "10001",
        country=parts[5] if len(parts) > 5 else "US",
    )

    if not args.session_cookie or not args.org_id:
        print("需要 --session-cookie 与 --org-id", file=sys.stderr)
        return 2

    r = bind_card_and_charge(
        session_cookie=args.session_cookie, org_id=args.org_id, card=card,
        amount=args.amount, proxy=args.proxy, dry_run=not args.live,
    )
    print(json.dumps({
        "ok": r.ok, "step": r.step, "source_id": r.source_id,
        "token": r.token[:12] + "..." if r.token else "", "error": r.error,
        "raw": r.raw,
    }, ensure_ascii=False, indent=2))
    return 0 if r.ok else 1


if __name__ == "__main__":
    raise SystemExit(main())
