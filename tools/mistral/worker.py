#!/usr/bin/env python3
"""
Mistral 注册 worker —— Go 服务通过 subprocess 调用

契约（与 perch-register/worker.py 一致）：
  stdin  : JSON
  stdout : 单行 JSON 结果
  stderr : 进度与诊断信息（Go 侧收集进 detail）

输入字段：
  {
    "email":       "user@icloud.com",
    "mailbox_url": "https://icloud.mida.vip/messages/...?token=...",
    "password":    "可选，缺省自动生成",
    "first":       "可选",
    "last":        "可选",
    "region":      "CA",
    "proxy":       "可选，指定单条代理 URL",
    "proxy_index": 0,           # 可选，按序号从代理池轮转取
    "skip_register": false      # true 表示账号已存在，只登录
  }

输出字段：
  {
    "ok": true,
    "email": "...",
    "password": "...",
    "org_uuid": "...",
    "workspace_uuid": "...",
    "keys": {"API": "mstrl_...", "Vibe": "...", "Studio": "..."},
    "vibe_cli_key": "",
    "status": "ok",
    "error": ""
  }

环境变量：
  ZOOPROXY_USER_TPL / ZOOPROXY_PASSWORD / ZOOPROXY_SIDS   代理池
  MISTRAL_WORKER_ROOT   代码根目录（默认上级目录）
"""
from __future__ import annotations

import json
import os
import sys
import traceback
from pathlib import Path

# core/ 在 signup/ 下；worker.py 与 signup/ 同级
ROOT = Path(os.environ.get("MISTRAL_WORKER_ROOT") or
            (Path(__file__).resolve().parent / "signup"))
sys.path.insert(0, str(ROOT))


def emit_progress(msg: str):
    """进度写 stderr，Go 侧会收集"""
    print(msg, file=sys.stderr, flush=True)


def main() -> int:
    try:
        raw = sys.stdin.read()
        req = json.loads(raw) if raw.strip() else {}
    except Exception as e:
        print(json.dumps({"ok": False, "error": f"bad input: {e}"}))
        return 2

    email = (req.get("email") or "").strip()
    mailbox_url = (req.get("mailbox_url") or "").strip()
    if not email or not mailbox_url:
        print(json.dumps({"ok": False, "error": "email and mailbox_url are required"}))
        return 2

    try:
        from core.hybrid import HybridRunner
        from runner import gen_name

        password = req.get("password") or ""
        if not password:
            from runner import gen_password
            password = gen_password()

        first = req.get("first") or ""
        last = req.get("last") or ""
        if not first or not last:
            first, last = gen_name(email)

        # proxy: 显式 URL 优先，其次按 proxy_index 从代理池轮转
        proxy = (req.get("proxy") or "").strip()
        if not proxy and req.get("proxy_index") is not None:
            from core.proxy_pool import ProxyPool
            px = ProxyPool().by_index(int(req.get("proxy_index") or 0))
            if px:
                from core.chain_gateway import ChainGateway
                gw = ChainGateway(pre_proxy="", target_proxy=px.url)
                port = gw.start()
                proxy = f"socks5://127.0.0.1:{port}"
                emit_progress(f"[proxy] index={req.get('proxy_index')} sid={px.sid} local={proxy}")

        runner = HybridRunner(headless=True)
        runner.debug(True)
        if proxy:
            # 单代理模式：绕过健康检查，直接用指定代理
            from core.proxy_pool import Proxy
            runner._forced_proxy = proxy
            orig = runner._prepare

            def _prepare_with_proxy(acct):
                acct.password = acct.password or password
                acct.first_name, acct.last_name = first, last
                acct.proxy_url = proxy
                runner._local = proxy
                runner._log(f"[prepare] forced proxy {proxy[:40]}...")

            runner._prepare = _prepare_with_proxy

        res = runner.run(
            email=email,
            mailbox_url=mailbox_url,
            password=password,
            first=first,
            last=last,
            region=req.get("region") or "CA",
            skip_register=bool(req.get("skip_register")),
        )

        out = {
            "ok": res.status == "ok",
            "email": res.email,
            "password": res.password,
            "org_uuid": res.org_uuid,
            "workspace_uuid": res.workspace_uuid,
            "keys": res.keys or {},
            "vibe_cli_key": res.vibe_cli_key,
            "sid": res.sid,
            "status": res.status,
            "error": res.error,
        }
        print(json.dumps(out, ensure_ascii=False))
        return 0 if out["ok"] else 1

    except Exception as e:
        traceback.print_exc(file=sys.stderr)
        print(json.dumps({
            "ok": False,
            "email": email,
            "status": "failed",
            "error": f"{type(e).__name__}: {e}",
        }, ensure_ascii=False))
        return 1


if __name__ == "__main__":
    sys.exit(main())
