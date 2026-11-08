#!/usr/bin/env python3
"""
纯协议注册机 CLI

设计:
  browser_provision()  -> 只用浏览器过一次 Cloudflare，拿 cookie
  protocol_*()         -> 之后全部走 HTTP，不再渲染页面

比 UI 路径快约 10-20 倍，适合批量。

用法:
  # 纯协议单账号（需先 provision 拿 cookie）
  python3 protocol_cli.py run --email X@icloud.com --mailbox "https://..."

  # 复用已 provision 的 cookie
  python3 protocol_cli.py resume --cookie-file data/cookies/X.json

  # 只跑 key 生成（已有 cookie）
  python3 protocol_cli.py keys --cookie-file ...

  # 取消订阅
  python3 protocol_cli.py cancel --cookie-file ... --plan API
"""
from __future__ import annotations

import argparse
import json
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))

from core.chain_gateway import GatewayPool, find_working_proxy
from core.fingerprint import build_profile, billing_address
from core.browser import BrowserOptions, SignupBrowser
from core.mailbox import ICloudMailbox, Mailbox
from core.proxy_pool import DEFAULT_SIDS, ProxyPool
from core.protocol import (
    AdminProtocol, ChatProtocol, ConsoleProtocol, KratosProtocol,
    ProtocolClient, ProtocolSession, parse_trpc,
)
from runner import gen_name, gen_password


# ---------------------------------------------------------------- provision
def provision(email: str, password: str, proxy_pool: ProxyPool,
              gw: GatewayPool, headless: bool = True,
              login_only: bool = False) -> dict:
    """
    用浏览器过一次 CF 并登录，导出 cookie。
    login_only=True 时假定账号已存在（走密码登录）。
    返回 {"cookies": {...}, "proxy": "socks5://...", "sid": "..."}
    """
    px, local = find_working_proxy(proxy_pool, gw, "https://v2.auth.mistral.ai/login")
    if not px:
        raise RuntimeError("no working proxy")

    prof = build_profile(email, "CA")
    with SignupBrowser(prof, local, BrowserOptions(headless=headless)) as sb:
        page = sb.page
        page.goto("https://v2.auth.mistral.ai/login",
                  wait_until="domcontentloaded", timeout=60000)
        page.wait_for_selector("input[name='email']", timeout=30000)
        sb.human_pause(1.5, 2.5)
        sb.human_type(page, "input[name='email']", email)
        page.get_by_role("button", name="Continue").first.click(timeout=10000)

        if login_only:
            page.wait_for_selector("input[name='password']", timeout=20000)
            sb.human_pause(0.6, 1.2)
            sb.human_type(page, "input[name='password']", password)
            sb.human_pause(0.4, 1.0)
            page.get_by_role("button", name="Continue with password").first.click(timeout=10000)
            page.wait_for_timeout(10000)

        cookies = {c["name"]: c["value"] for c in sb.context.cookies()}
        return {"cookies": cookies, "proxy": local, "sid": px.sid,
                "url": page.url}


def make_client(prov: dict, email: str, region: str = "CA") -> ProtocolClient:
    prof = build_profile(email, region)
    sess = ProtocolSession(cookies=prov["cookies"], headers=prof.headers(),
                           proxy=prov["proxy"])
    return ProtocolClient(sess)


# ---------------------------------------------------------------- 命令
def cmd_run(a):
    pool = ProxyPool(DEFAULT_SIDS)
    gw = GatewayPool()
    email = a.email
    password = a.password or gen_password()
    first, last = gen_name(email)

    print(f"==> {email}")
    print(f"    password: {password}")

    # 阶段 1: 注册（Kratos 可以纯 HTTP，不需要先登录）
    # 但需要 CF cookie，所以先让浏览器访问一次拿 __cf_bm
    prov = provision(email, password, pool, gw, login_only=False)
    print(f"    proxy: {prov['sid']}  cookies: {len(prov['cookies'])}")
    cli = make_client(prov, email)

    k = KratosProtocol(cli)
    reg = k.register(email, password, first, last)
    cli.harvest_cookies()
    print(f"[register] identity={reg['identity_id']}")

    # 阶段 2: 触发验证码
    flow = k.start_verification(email)
    print("[verify] code requested")

    # 阶段 3: 从邮箱取码
    mbx = ICloudMailbox(Mailbox.from_line(f"{email}----{a.mailbox}"),
                        proxy="socks5h://127.0.0.1:10808")
    baseline = set()
    j = mbx.fetch("all")
    if j.get("ok"):
        baseline = {e.get("id") for e in (j.get("data") or {}).get("emails", [])}

    code = None
    import re
    for _ in range(48):
        time.sleep(5)
        j = mbx.fetch("all")
        if not j.get("ok"):
            continue
        for e in (j.get("data") or {}).get("emails", []):
            if e.get("id") in baseline:
                continue
            blob = ((e.get("fromAddress") or "") + (e.get("subject") or "")).lower()
            if "mistral" not in blob:
                continue
            m = re.search(r"\b(\d{6})\b", e.get("textContent") or "")
            if m:
                code = m.group(1)
                break
        if code:
            break
    if not code:
        print("[verify] FAILED: no code")
        return 1
    print(f"[verify] code={code}")

    # flow 已在 start_verification 里刷新过（含可用 csrf）
    ver = k.submit_code(flow, code)
    cli.harvest_cookies()
    print(f"[verify] passed={ver['passed']}")
    if not ver["passed"]:
        return 1

    # 阶段 4: 纯协议完成后续（需要先登录一次拿 admin 会话）
    prov = provision(email, password, pool, gw, login_only=True)
    cli = make_client(prov, email)
    cli.harvest_cookies()

    result = finish_protocol(cli, email)
    result["password"] = password
    result["sid"] = prov["sid"]

    _save(a.out, email, result, prov)
    print(json.dumps(result, indent=1, ensure_ascii=False))
    gw.close_all()
    return 0


def finish_protocol(cli: ProtocolClient, email: str,
                    do_tos: bool = True, do_privacy: bool = True) -> dict:
    """登录之后的全部纯协议动作"""
    a = AdminProtocol(cli)
    c = ConsoleProtocol(cli)
    ch = ChatProtocol(cli)
    out: dict = {"email": email}

    if do_tos:
        out["studio_tos"] = c.accept_studio_tos().get("status")
        t = ch.accept_tos(newsletter=True)
        out["chat_tos"] = t.get("tosAccepted")

    if do_privacy:
        ch.set_message_training(False)
        ch.set_knowledge(False)
        ch.set_localisation(False)
        out["privacy"] = "locked-down"

    me = a.me_session()
    out["org_uuid"] = (me.get("organization") or {}).get("uuid")
    out["workspace_uuid"] = (me.get("workspace") or {}).get("uuid")

    out["keys"] = {}
    if out["workspace_uuid"]:
        for prod in ("API", "Vibe", "Studio"):
            r = a.create_api_key(out["workspace_uuid"], product=prod)
            out["keys"][prod] = r.get("key", r)

    # Vibe CLI Key（code-trpc，独立于上面三把）
    try:
        vk = ch.generate_vibe_key()
        out["vibe_cli_key"] = vk.get("vibeApiKey")
        out["vibe_cli_key_id"] = vk.get("vibeApiKeyId")
    except Exception as e:
        out["vibe_cli_key"] = f"err: {e}"

    return out


def _save(outdir: str, email: str, result: dict, prov: dict):
    d = Path(outdir)
    d.mkdir(parents=True, exist_ok=True)
    (d / "accounts.json").exists() and None
    f = d / "accounts.json"
    rows = []
    if f.exists():
        try:
            rows = json.loads(f.read_text())
        except Exception:
            rows = []
    rows = [r for r in rows if r.get("email") != email] + [result]
    f.write_text(json.dumps(rows, indent=1, ensure_ascii=False))
    (d / f"cookie_{email.split('@')[0]}.json").write_text(
        json.dumps(prov, indent=1))


def cmd_resume(a):
    prov = json.loads(Path(a.cookie_file).read_text())
    cli = make_client(prov, a.email)
    r = finish_protocol(cli, a.email)
    print(json.dumps(r, indent=1, ensure_ascii=False))
    return 0


def cmd_keys(a):
    prov = json.loads(Path(a.cookie_file).read_text())
    cli = make_client(prov, a.email)
    adm = AdminProtocol(cli)
    me = adm.me_session()
    ws = (me.get("workspace") or {}).get("uuid")
    print("workspace:", ws)
    for prod in ("API", "Vibe", "Studio"):
        r = adm.create_api_key(ws, product=prod)
        print(f"{prod:7s}: {r.get('key', r)}")
    return 0


def cmd_cancel(a):
    prov = json.loads(Path(a.cookie_file).read_text())
    cli = make_client(prov, a.email)
    adm = AdminProtocol(cli)
    print("取消订阅:", adm.cancel_subscription(a.plan))
    return 0


def main():
    p = argparse.ArgumentParser()
    sub = p.add_subparsers(dest="cmd", required=True)

    r = sub.add_parser("run")
    r.add_argument("--email", required=True)
    r.add_argument("--mailbox", required=True)
    r.add_argument("--password", default="")
    r.add_argument("--out", default="data")
    r.add_argument("--headless", action="store_true", default=True)

    s = sub.add_parser("resume")
    s.add_argument("--cookie-file", required=True)
    s.add_argument("--email", required=True)

    k = sub.add_parser("keys")
    k.add_argument("--cookie-file", required=True)
    k.add_argument("--email", required=True)

    c = sub.add_parser("cancel")
    c.add_argument("--cookie-file", required=True)
    c.add_argument("--email", required=True)
    c.add_argument("--plan", default="API")

    a = p.parse_args()
    return {"run": cmd_run, "resume": cmd_resume,
            "keys": cmd_keys, "cancel": cmd_cancel}[a.cmd](a)


if __name__ == "__main__":
    sys.exit(main())
