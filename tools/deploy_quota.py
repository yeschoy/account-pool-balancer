#!/usr/bin/env python3
"""
deploy_quota.py —— 号池额度查询（纯 HTTP，不需要浏览器）

核心结论（2026-11-15 实测）
──────────────────────────
deploy 的会话就一个 cookie：

    hive-portal-production=<438-444 字符>

把它从 AdsPower profile 里取出来，**在任何机器上直连都能用**：

    GET https://ajax-portal.deploy.com/api/organization/{org}/credit
    Cookie: hive-portal-production=...

实测从 OVH 用 Mac profile 里取出的 cookie 查询，返回 200 且数据完整。

所以额度查询完全不需要走 CDP 隧道 —— 那是给绑卡这种「必须真浏览器」的
操作用的。查询是纯读，用 HTTP 足够。

数据流
──────
    Mac (AdsPower profile)              OVH (号池)
      │ 提取 cookie                        │
      └──────── 同步（一次） ─────────────►│ 入库
                                          │
                                          └─ 定时用 cookie 查额度
                                             （纯 HTTP，高频、低成本）

额度口径
────────
GET /api/organization/{org}/credit 返回**流水数组**，每条形如：

    {"charged_on":"...","amount":"100.00","reason":"purchase_promo"}
    {"charged_on":"...","amount":"5.00","reason":"bought"}
    {"charged_on":"...","amount":"1.00","reason":"initial"}

余额 = 各条 amount 之和。实测 reason 有：initial / bought / purchase_promo。

⚠️ 这是「账户余额」，不是「模型调用额度」。模型 API 的配额限制
（RPM/TPM）需要另外查，见 --models。
"""

from __future__ import annotations

import argparse
import json
import sys
import time
import urllib.error
import urllib.request
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path

BASE = "https://ajax-portal.deploy.com"
ORIGIN = "https://deploy.com"
SESSION_COOKIE = "hive-portal-production"

HERE = Path(__file__).resolve().parent


# --------------------------------------------------------------------------
# 结果
# --------------------------------------------------------------------------

@dataclass
class Quota:
    ok: bool
    org_id: str = ""
    balance: float | None = None
    rows: list[dict] = field(default_factory=list)
    error: str = ""
    checked_at: str = ""

    def as_dict(self) -> dict:
        return {
            "ok": self.ok,
            "org_id": self.org_id,
            "balance": self.balance,
            "rows": self.rows,
            "error": self.error,
            "checked_at": self.checked_at,
        }


# --------------------------------------------------------------------------
# HTTP（纯 cookie，无浏览器）
# --------------------------------------------------------------------------

class CookieClient:
    """只用 session cookie 的轻量客户端，可在任意机器运行。"""

    def __init__(self, session_cookie: str, timeout: float = 30.0,
                 user_agent: str = (
                     "Mozilla/5.0 (Windows NT 10.0; Win64; x64) "
                     "AppleWebKit/537.36 (KHTML, like Gecko) "
                     "Chrome/153.0.0.0 Safari/537.36")) -> None:
        self.cookie = session_cookie.strip()
        self.timeout = timeout
        self.ua = user_agent

    def _headers(self) -> dict:
        return {
            "Accept": "application/json, text/plain, */*",
            "Origin": ORIGIN,
            "Referer": "https://deploy.com/",
            "Cookie": f"{SESSION_COOKIE}={self.cookie}",
            "User-Agent": self.ua,
            "X-Forwarded-Proto": "https",
        }

    def get(self, path: str) -> tuple[int, str]:
        req = urllib.request.Request(BASE + path, headers=self._headers())
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as r:
                return r.status, r.read().decode("utf-8", "replace")
        except urllib.error.HTTPError as e:
            return e.code, e.read().decode("utf-8", "replace")
        except urllib.error.URLError as e:
            return 0, f"network error: {e.reason}"

    def post(self, path: str, payload: dict) -> tuple[int, str]:
        data = json.dumps(payload).encode()
        h = self._headers()
        h["Content-Type"] = "application/json"
        req = urllib.request.Request(BASE + path, data=data, headers=h,
                                     method="POST")
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as r:
                return r.status, r.read().decode("utf-8", "replace")
        except urllib.error.HTTPError as e:
            return e.code, e.read().decode("utf-8", "replace")
        except urllib.error.URLError as e:
            return 0, f"network error: {e.reason}"


# --------------------------------------------------------------------------
# 额度
# --------------------------------------------------------------------------

def fetch_quota(session_cookie: str, org_id: str | int) -> Quota:
    """查账户余额（流水求和）。失败时 balance=None，绝不返回 0 糊弄。"""
    now = datetime.now(timezone.utc).isoformat()
    cli = CookieClient(session_cookie)
    st, body = cli.get(f"/api/organization/{org_id}/credit")
    if st == 0:
        return Quota(False, str(org_id), None, [], body[:120], now)
    try:
        j = json.loads(body)
    except Exception:  # noqa: BLE001
        return Quota(False, str(org_id), None, [], f"响应无法解析: {body[:80]}", now)

    if st != 200:
        msg = (j.get("message") if isinstance(j, dict) else "") or body[:120]
        return Quota(False, str(org_id), None, [], f"HTTP {st}: {msg}", now)

    rows = j.get("data")
    if not isinstance(rows, list):
        return Quota(False, str(org_id), None, [], "data 不是数组", now)

    try:
        total = round(sum(float(r.get("amount") or 0)
                          for r in rows if isinstance(r, dict)), 2)
    except Exception:  # noqa: BLE001
        return Quota(False, str(org_id), None, rows, "金额求和失败", now)

    return Quota(True, str(org_id), total, rows, "", now)


def fetch_profile(session_cookie: str) -> dict | None:
    """用 cookie 拿账号信息（含 org_id），用于自动填充 OrgId。"""
    cli = CookieClient(session_cookie)
    st, body = cli.get("/api/profile")
    if st != 200:
        return None
    try:
        d = (json.loads(body).get("data") or {})
    except Exception:  # noqa: BLE001
        return None
    org = (d.get("personal_organization") or {}).get("organization_id")
    return {
        "account_id": d.get("id"),
        "email": d.get("email"),
        "is_verified": bool(d.get("is_verified")),
        "org_id": org,
    }


def check_alive(session_cookie: str) -> tuple[bool, str]:
    """会话是否还有效。返回 (alive, 说明)。"""
    cli = CookieClient(session_cookie)
    st, body = cli.get("/api/profile")
    if st == 200:
        return True, "ok"
    if st == 401:
        return False, "会话失效（401）"
    if st == 0:
        return False, f"网络异常: {body[:80]}"
    return False, f"HTTP {st}"


# --------------------------------------------------------------------------
# 模型 API 配额（需要 access_key，不走 cookie）
# --------------------------------------------------------------------------

MODEL_API = "https://api.deploy.com/api/v1"


def list_models(secret_key: str, timeout: float = 30.0) -> tuple[bool, str]:
    """用 Secret Key 探测模型 API 是否可用。"""
    req = urllib.request.Request(
        f"{MODEL_API}/models",
        headers={"Authorization": f"Bearer {secret_key}",
                 "Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return True, f"HTTP {r.status}"
    except urllib.error.HTTPError as e:
        return False, f"HTTP {e.code}: {e.read()[:100]}"
    except Exception as e:  # noqa: BLE001
        return False, f"{type(e).__name__}: {e}"


# --------------------------------------------------------------------------
# 从 AdsPower profile 提取 cookie（需在本机执行）
# --------------------------------------------------------------------------

def extract_cookie_from_profile(api_key: str, profile_id: str,
                                org_hint: str | int | None = None) -> str | None:
    """
    在本机（装了 AdsPower 的机器）提取会话 cookie。

    ⚠️ 必须在 Mac 上跑：cookie 在 profile 的本地存储里。
    提取后把**字符串**同步到 OVH 入库，之后查询就不需要 Mac 了。
    """
    sys.path.insert(0, str(HERE))
    import deploy_registrar as R  # noqa: PLC0415

    try:
        c = R.AdsHttpClient(api_key, profile_id)
    except Exception:  # noqa: BLE001
        return None
    # 先打一次 profile，浏览器才会把 cookie 写进可读状态
    c.request("GET", "https://ajax-portal.deploy.com/api/profile")
    c.refresh_cookies()
    sess = c.cookies.get(SESSION_COOKIE, "")
    return sess or None


# --------------------------------------------------------------------------

def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description="deploy 号池额度查询")
    ap.add_argument("--cookie", help="session cookie 值")
    ap.add_argument("--org", help="org_id（不给则用 /api/profile 自动取）")
    ap.add_argument("--file", help="账号文件 jsonl，每行含 session_cookie/org_id")
    ap.add_argument("--extract", nargs=2, metavar=("API_KEY", "PROFILE_ID"),
                    help="从本机 AdsPower profile 提取 cookie")
    ap.add_argument("--alive", action="store_true", help="只检查会话是否有效")
    ap.add_argument("--json", action="store_true")
    args = ap.parse_args(argv)

    # 提取模式
    if args.extract:
        key, pid = args.extract
        sess = extract_cookie_from_profile(key, pid)
        if not sess:
            print("提取失败：profile 未登录或无会话", file=sys.stderr)
            return 1
        print(sess)
        return 0

    # 批量模式
    if args.file:
        p = Path(args.file)
        if not p.exists():
            print(f"文件不存在: {p}", file=sys.stderr)
            return 1
        out = []
        for line in p.read_text(encoding="utf-8").splitlines():
            line = line.strip()
            if not line:
                continue
            try:
                it = json.loads(line)
            except Exception:  # noqa: BLE001
                continue
            sess = it.get("session_cookie") or ""
            org = it.get("org_id") or ""
            if not sess:
                out.append({"id": it.get("id"), "ok": False,
                            "error": "无 session_cookie"})
                continue
            if not org:
                pv = fetch_profile(sess)
                org = (pv or {}).get("org_id") or ""
            q = fetch_quota(sess, org) if org else Quota(
                False, "", None, [], "无 org_id")
            out.append({"id": it.get("id"), "email": it.get("email"),
                        **q.as_dict()})
            time.sleep(0.3)   # 温和节奏
        print(json.dumps(out, ensure_ascii=False, indent=1))
        return 0

    if not args.cookie:
        print("需要 --cookie 或 --extract 或 --file", file=sys.stderr)
        return 1

    if args.alive:
        alive, why = check_alive(args.cookie)
        print(json.dumps({"alive": alive, "reason": why},
                         ensure_ascii=False))
        return 0 if alive else 1

    org = args.org
    if not org:
        pv = fetch_profile(args.cookie)
        if pv:
            org = str(pv.get("org_id") or "")
            print(f"账号: {pv.get('email')} (id={pv.get('account_id')}, "
                  f"org={org})", file=sys.stderr)
    if not org:
        print("拿不到 org_id", file=sys.stderr)
        return 1

    q = fetch_quota(args.cookie, org)
    if args.json:
        print(json.dumps(q.as_dict(), ensure_ascii=False, indent=1))
    else:
        if not q.ok:
            print(f"查询失败: {q.error}")
            return 1
        print(f"org_id : {q.org_id}")
        print(f"余额   : ${q.balance:.2f}")
        print(f"流水   : {len(q.rows)} 条")
        for r in q.rows:
            print(f"  {str(r.get('charged_on'))[:19]}  "
                  f"{float(r.get('amount') or 0):>9.2f}  {r.get('reason')}")
    return 0 if q.ok else 1


if __name__ == "__main__":
    raise SystemExit(main())
