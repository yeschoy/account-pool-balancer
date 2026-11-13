#!/usr/bin/env python3
"""Batch runner for essevin registrations.

Input files (one per line, '#' comments ignored):

  accounts.txt    email----share_url
  proxies.txt     socks5://user:pass@host:port   (or http://...)

Each account gets exactly one proxy and one identity, both derived from the
line index so a retry reuses the same pair. Results are appended to
account-pool.jsonl as they complete, so an interrupted run can resume.

Usage:
  python3 run_batch.py --accounts accounts.txt --proxies proxies.txt \
      --upstream-hop http://127.0.0.1:10808 --out account-pool.jsonl --concurrency 2
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import threading
import time
import traceback
from concurrent.futures import ThreadPoolExecutor

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from registrar import (  # noqa: E402
    IdentityPool, ProxyEndpoint, Registrar, RegError, check_egress,
)

HERE = os.path.dirname(os.path.abspath(__file__))


def read_lines(path: str) -> list[str]:
    out = []
    with open(path, encoding="utf-8") as fh:
        for raw in fh:
            line = raw.strip()
            if line and not line.startswith("#"):
                out.append(line)
    return out


def load_accounts(path: str) -> list[tuple[str, str]]:
    rows = []
    for line in read_lines(path):
        if "----" not in line:
            raise SystemExit(f"bad account line (need email----url): {line[:40]}")
        email, url = line.split("----", 1)
        rows.append((email.strip(), url.strip()))
    return rows


def load_proxies(path: str, hop: str | None) -> list[ProxyEndpoint]:
    out = []
    for i, line in enumerate(read_lines(path)):
        out.append(ProxyEndpoint.parse(f"px{i}", line, upstream_http=hop))
    return out


def handle(args, idx: int, email: str, share_url: str,
           proxies: list[ProxyEndpoint], lock: threading.Lock) -> dict:
    px = proxies[idx % len(proxies)] if proxies else None
    ident_pool = IdentityPool(seed=f"essevin:{email}".encode())
    identity = ident_pool.pick()
    reg = Registrar(identity=identity, proxy=px,
                    log=lambda s: None)
    started = time.time()
    rec: dict = {"email": email, "ok": False, "started_at": time.strftime("%FT%TZ", time.gmtime(started))}
    try:
        res = reg.run(email=email, share_url=share_url, key_name=args.key_name,
                      mail_timeout=args.mail_timeout)
        rec.update({
            "ok": True,
            "user_id": res.user_id,
            "api_key": res.api_key,
            "api_key_id": res.api_key_id,
            "session_token": res.token,
            "balance": res.balance,
            "egress_ip": res.egress_ip,
            "proxy": res.proxy,
            "identity": identity.user_agent,
            "warn": res.warn,
        })
    except RegError as exc:
        rec.update({"error": exc.code, "message": exc.message, "status": exc.status})
    except Exception as exc:
        rec.update({"error": "UNEXPECTED", "message": repr(exc),
                    "trace": traceback.format_exc()[-1200:]})
    rec["elapsed_s"] = round(time.time() - started, 1)
    with lock:
        with open(args.out, "a", encoding="utf-8") as fh:
            fh.write(json.dumps(rec, ensure_ascii=False) + "\n")
        tag = "OK " if rec["ok"] else "ERR"
        print(f"[{tag}] {email} {rec.get('error', '')} {rec['elapsed_s']}s", flush=True)
    return rec


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--accounts", default=os.path.join(HERE, "accounts.txt"))
    ap.add_argument("--proxies", default=os.path.join(HERE, "proxies.txt"))
    ap.add_argument("--upstream-hop", default="http://127.0.0.1:10808",
                    help="local HTTP CONNECT proxy used to reach each upstream hop "
                         "(needed when the provider allowlists source IPs)")
    ap.add_argument("--out", default=os.path.join(HERE, "account-pool.jsonl"))
    ap.add_argument("--concurrency", type=int, default=1)
    ap.add_argument("--key-name", default="auto")
    ap.add_argument("--mail-timeout", type=float, default=180.0)
    ap.add_argument("--check", action="store_true", help="only verify proxy egress, register nothing")
    args = ap.parse_args()

    accounts = load_accounts(args.accounts)
    proxies = load_proxies(args.proxies, args.upstream_hop or None) if os.path.exists(args.proxies) else []
    print(f"accounts={len(accounts)} proxies={len(proxies)} hop={args.upstream_hop or 'none'}")

    if args.check:
        for i, px in enumerate(proxies):
            try:
                print(f"{px.name:5s} {px.describe():55s} exit={check_egress(px)}")
            except Exception as exc:
                print(f"{px.name:5s} {px.describe():55s} FAIL {exc}")
        return 0

    lock = threading.Lock()
    with ThreadPoolExecutor(max_workers=max(1, args.concurrency)) as pool:
        futures = [pool.submit(handle, args, i, e, u, proxies, lock)
                   for i, (e, u) in enumerate(accounts)]
        for f in futures:
            f.result()
    ok = sum(1 for line in open(args.out, encoding="utf-8")
             if json.loads(line).get("ok")) if os.path.exists(args.out) else 0
    print(f"\n完成: 成功 {ok} / {len(accounts)}   结果: {args.out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
