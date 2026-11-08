#!/usr/bin/env python3
"""
Mistral 注册机 CLI

用法:
  # 单账号：注册 + 接码（不开浏览器窗口）
  python3 cli.py run --email beards-armors.19@icloud.com \
      --mailbox "https://icloud.mida.vip/messages/beards-armors.19%40icloud.com?token=..." \
      --headless

  # 批量
  python3 cli.py batch --accounts accounts.txt --limit 5

  # 只生成 Key（已有会话）
  python3 cli.py keys --email ...

账号文件格式（每行）:
  email----https://icloud.mida.vip/messages/<encoded-email>?token=<token>
"""
from __future__ import annotations

import argparse
import os
import json
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))

from core.chain_gateway import GatewayPool
from core.mailbox import Mailbox, load_mailboxes
from core.proxy_pool import DEFAULT_SIDS, ProxyPool
from runner import Account, SignupRunner, Store, gen_password


# 代理凭据从环境变量读取（不写死在代码里）
#   export ZOOPROXY_USER_TPL='<user>-region-{region}-sid-{sid}-t-10'
#   export ZOOPROXY_PASSWORD='<password>'
#   export ZOOPROXY_SIDS='sid1,sid2,...'      # 可选


def _load_sids():
    import os
    raw = os.environ.get("ZOOPROXY_SIDS", "")
    return [s for s in raw.split(",") if s] or DEFAULT_SIDS


def parse_args():
    p = argparse.ArgumentParser(description="Mistral signup runner")
    sub = p.add_subparsers(dest="cmd", required=True)

    r = sub.add_parser("run", help="单账号")
    r.add_argument("--email", required=True)
    r.add_argument("--mailbox", required=True, help="接码链接")
    r.add_argument("--password", default="")
    r.add_argument("--region", default="CA")
    r.add_argument("--headless", action="store_true")
    r.add_argument("--subscribe", action="store_true")
    r.add_argument("--store", default="data/accounts.json")

    b = sub.add_parser("batch", help="批量")
    b.add_argument("--accounts", required=True)
    b.add_argument("--limit", type=int, default=1)
    b.add_argument("--region", default="CA")
    b.add_argument("--headless", action="store_true")
    b.add_argument("--store", default="data/accounts.json")

    t = sub.add_parser("test-proxy", help="测试代理池")
    t.add_argument("--all", action="store_true")

    s = sub.add_parser("test-mail", help="测试接码")
    s.add_argument("--email", required=True)
    s.add_argument("--mailbox", required=True)

    return p.parse_args()


def make_runner(headless: bool) -> SignupRunner:
    pool = ProxyPool(DEFAULT_SIDS)
    return SignupRunner(pool, GatewayPool(), headless=headless)


def cmd_run(a):
    runner = make_runner(a.headless)
    store = Store(a.store)
    acct = Account(email=a.email, mailbox_url=a.mailbox,
                   password=a.password, region=a.region)
    print(f"==> 开始注册 {a.email}")
    res = runner.run(acct, do_subscribe=a.subscribe)
    store.upsert(res)
    print(json.dumps(res.to_dict(), indent=1, ensure_ascii=False))
    runner.gw.close_all()
    return 0 if res.status in ("ok", "complete") else 1


def cmd_batch(a):
    boxes = load_mailboxes(a.accounts)
    store = Store(a.store)
    done = store.done_emails()
    todo = [b for b in boxes if b.email not in done][: a.limit]
    print(f"==> 待处理 {len(todo)} / 总 {len(boxes)}")
    runner = make_runner(a.headless)
    ok = 0
    for i, mb in enumerate(todo, 1):
        print(f"\n[{i}/{len(todo)}] {mb.email}")
        acct = Account(email=mb.email, mailbox_url=mb.url, region=a.region)
        res = runner.run(acct)
        store.upsert(res)
        if res.status in ("ok", "complete"):
            ok += 1
        print(f"    -> {res.status} {res.error}")
        time.sleep(5)
    runner.gw.close_all()
    print(f"\n完成 {ok}/{len(todo)}")
    return 0


def cmd_test_proxy(a):
    import subprocess
    pool = ProxyPool(DEFAULT_SIDS)
    gw = GatewayPool()
    print("测试代理链（本地 -> zooproxy -> ip-api）:")
    for p in pool.all():
        local = gw.get(p.url)
        try:
            r = subprocess.run(
                ["curl", "-s", "-m", "30", "--socks5-hostname", local.replace("socks5://", ""),
                 "http://ip-api.com/json/?fields=query,country,isp"],
                capture_output=True, text=True)
            j = json.loads(r.stdout) if r.stdout.strip() else {}
            ok = "query" in j
            print(f"  {p.sid:12s} {'OK ' if ok else 'FAIL'} {j.get('query',''):16s} "
                  f"{j.get('country','')} {j.get('isp','')[:28]}")
        except Exception as e:
            print(f"  {p.sid:12s} FAIL {e}")
    gw.close_all()
    return 0


def cmd_test_mail(a):
    from core.mailbox import ICloudMailbox
    mb = Mailbox.from_line(f"{a.email}----{a.mailbox}")
    m = ICloudMailbox(mb, proxy=os.environ.get("MIDA_PROXY") or None)
    j = m.fetch("all")
    if not j.get("ok"):
        print("FAIL:", j)
        return 1
    d = j["data"]
    print(f"邮箱: {d['aliasEmail']}  过期: {d['expiresAt']}  状态: {d['shareStatus']}")
    print(f"邮件数: {len(d['emails'])}")
    for e in d["emails"][:5]:
        print(f"  [{e['id']}] {e['subject'][:60]}")
    return 0


def main():
    a = parse_args()
    return {
        "run": cmd_run,
        "batch": cmd_batch,
        "test-proxy": cmd_test_proxy,
        "test-mail": cmd_test_mail,
    }[a.cmd](a)


if __name__ == "__main__":
    sys.exit(main())
