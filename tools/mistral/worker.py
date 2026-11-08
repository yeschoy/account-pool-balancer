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
    "card_masked": "463343******7213",   # 分配到的卡（脱敏）
    "card_expiry": "05/28",
    "error": ""
  }

环境变量：
  MISTRAL_PROXY_URLS   代理列表，换行分隔的完整 URL
                       （socks5://user:pass@ip:port，zooproxy 同格式）
  MISTRAL_CARDS        银行卡列表，换行分隔，格式 卡号----有效期----CVV
                       例如 4633433883347213----05/28----913
                       仅包含通过位数与 Luhn 校验的行；供后续订阅步骤使用，
                       当前注册流程不读取它。
  MISTRAL_PACE_SCALE   节奏缩放系数，默认 1.0。0 表示关闭全部停顿（仅测试用）。
                       对 lognormal 采样结果整体相乘，不改变各步比例。
  MISTRAL_PACE_OFF     设为 1 时完全关闭停顿，优先于 SCALE。
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
        from core.cards import cards_from_env, pick_card
        from runner import gen_name

        password = req.get("password") or ""
        if not password:
            from runner import gen_password
            password = gen_password()

        first = req.get("first") or ""
        last = req.get("last") or ""
        if not first or not last:
            first, last = gen_name(email)

        # 银行卡：按 proxy_index 轮转选一张，与 Go 侧 PickCardByIndex 对齐。
        # 当前仅解析并回显，注册流程本身不用；订阅步骤后续从这里取。
        card = None
        cards = cards_from_env()
        if cards:
            idx = int(req.get("proxy_index") or 0)
            card = pick_card(cards, idx)
            if card:
                emit_progress(f"[card] 已分配 {card.masked()} exp={card.expiry}")

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
            # 分配到的卡（脱敏，仅用于展示与后续订阅关联）
            "card_masked": card.masked() if card else "",
            "card_expiry": card.expiry if card else "",
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
