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
    "exit_ip": "203.0.113.7",   # 本次实际出口 IP（cloudflare trace 实测）
    "country": "CA",            # 画像/账单地址依据的出口国
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
import shutil
import subprocess
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


# ---------------------------------------------------------------- 虚拟显示
#
# 浏览器必须 headed：新 headless 的 WebGL 是 SwiftShader，
# 与画像声称的真实 GPU 直接矛盾，一条 getParameter(37446) 就判死。
# Linux 容器里用 Xvfb 提供一个无头显示，Chrome 自己以为是 headed。
_XVFB: subprocess.Popen | None = None


def ensure_display() -> str:
    """确保有 DISPLAY；没有就在本地起一个 Xvfb。返回 DISPLAY 值。"""
    global _XVFB
    disp = os.environ.get("DISPLAY", "").strip()
    if disp:
        return disp
    if os.name == "nt" or sys.platform == "darwin":
        # macOS/Windows 有真实窗口服务，不需要 Xvfb
        return ""

    xvfb = shutil.which("Xvfb") or shutil.which("xvfb-run")
    if not xvfb:
        emit_progress("[display] 未找到 Xvfb，浏览器会退到 headless（WebGL 将暴露）")
        return ""

    num = int(os.environ.get("MISTRAL_XVFB_DISPLAY") or 99)
    for _ in range(20):
        sock = f"/tmp/.X11-unix/X{num}"
        if os.path.exists(sock) and _probe_display(f":{num}"):
            os.environ["DISPLAY"] = f":{num}"
            return f":{num}"
        try:
            proc = subprocess.Popen(
                [xvfb, f":{num}", "-screen", "0", "1920x1080x24", "-nolisten", "tcp"],
                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        except Exception as e:
            emit_progress(f"[display] Xvfb 启动失败: {e}")
            return ""
        import time as _t
        for _ in range(30):
            _t.sleep(0.1)
            if os.path.exists(sock) and proc.poll() is None:
                _XVFB = proc
                os.environ["DISPLAY"] = f":{num}"
                emit_progress(f"[display] Xvfb 已启动 DISPLAY=:{num}")
                return f":{num}"
        if proc.poll() is not None:
            num += 1
            continue
        break
    emit_progress("[display] Xvfb 起不来，退到 headless")
    return ""


def _probe_display(disp: str) -> bool:
    xdpy = shutil.which("xdpyinfo")
    if not xdpy:
        return True  # 没有探测工具就假设可用
    try:
        r = subprocess.run([xdpy, "-display", disp], capture_output=True, timeout=5)
        return r.returncode == 0
    except Exception:
        return False


def stop_display():
    global _XVFB
    if _XVFB is not None:
        try:
            _XVFB.terminate()
        except Exception:
            pass
        _XVFB = None


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

        # headed 真 Chrome 需要一个显示（Linux 容器里由 Xvfb 提供）
        ensure_display()

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

        # ---------- 代理 + 出口 IP 去重 ----------
        #
        # 一 IP 一号：同一个出口 IP 只允许注册一个账号。
        # 旧实现是 by_index(n) = proxies[n % len]，10 个代理 40 个号 =>
        # 每个住宅 IP 4 个号，串行连续跑，IP 信誉直接打穿。
        from core.chain_gateway import GatewayPool, probe_exit
        from core.proxy_pool import ProxyPool

        used = [str(x) for x in (req.get("used_exit_ips") or []) if x]
        explicit = (req.get("proxy") or "").strip()
        start = int(req.get("proxy_index") or 0)

        pool = ProxyPool()
        gw_pool = GatewayPool()
        chosen, geo = "", {}

        if explicit:
            local = gw_pool.get(explicit)
            geo = probe_exit(local)
            if geo.get("ip") and geo["ip"] in used:
                raise RuntimeError(
                    f"出口 IP {geo['ip']} 已被其他账号使用（一 IP 一号），拒绝复用")
            chosen = explicit
            emit_progress(f"[proxy] 指定代理 exit={geo.get('ip')} country={geo.get('country')}")
        else:
            for attempt in range(8):
                px = pool.by_index(start + attempt)
                if not px:
                    break
                local = gw_pool.get(px.url)
                geo = probe_exit(local)
                if not geo.get("ok"):
                    emit_progress(f"[proxy] {px.sid} 探测失败: {geo.get('error')}")
                    continue
                if geo["ip"] in used:
                    emit_progress(f"[proxy] {px.sid} exit={geo['ip']} 已用过，跳过")
                    continue
                chosen = px.url
                emit_progress(
                    f"[proxy] index={start + attempt} sid={px.sid} "
                    f"exit={geo['ip']} country={geo.get('country')}")
                break
            if not chosen:
                raise RuntimeError("没有可用的未占用出口 IP（一 IP 一号）")

        # headless=False：走 xvfb 里的 headed 真 Chrome，WebGL 是真 GPU
        runner = HybridRunner(pool=pool, gw=gw_pool, headless=False)
        runner.debug(True)

        res = runner.run(
            email=email,
            mailbox_url=mailbox_url,
            password=password,
            first=first,
            last=last,
            region=req.get("region") or "CA",
            skip_register=bool(req.get("skip_register")),
            forced_proxy=chosen,
            exit_geo=geo,
            exclude_ips=used,
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
            "exit_ip": res.exit_ip,
            "country": res.country,
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
    try:
        sys.exit(main())
    finally:
        stop_display()
