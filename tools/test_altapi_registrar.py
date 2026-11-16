#!/usr/bin/env python3
"""altapi 注册机与 Go 端规则一致性测试。

normalize_uid / session_value 在 Python 和 Go 里各有一份实现（注册机是独
立子进程，import 不到 Go 包）。这个测试和 internal/newapiconsole/cases_test.go
读同一张用例表，任何一边改了规则没同步，对应的那条测试立刻失败。

运行:
  python3 tools/test_altapi_registrar.py
也可以被 go test 之外的 CI 直接调用，退出码非 0 即失败。
"""

from __future__ import annotations

import ast
import json
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
CASES = HERE.parent / "internal" / "newapiconsole" / "cases.json"
REGISTRAR = HERE / "altapi_registrar.py"

# 只有这两条规则在 Python 里有对应实现：余额换算只发生在 Go 端，
# 所以 balance_usd 只由 Go 测试校验，这里跳过（不是遗漏）。
WANTED = ("normalize_uid", "session_value", "remote_dns_proxy")

# remote_dns_proxy 只有 Python 一份（Go 的 SOCKS 拨号器天然远端解析，无需转换），
# 用例直接写在这里。背景：socks5:// 让 libcurl 本地解析 DNS，代理按裸 IP 连
# altapi.ai 会被 Cloudflare 掐断 TLS（curl 35），线上注册机 2026-11-16 实际踩过。
PROXY_CASES = [
    ("socks5://u:p@51.222.52.202:1080", "socks5h://u:p@51.222.52.202:1080", "socks5 必须改远端解析"),
    ("SOCKS5://u:p@h:1080", "socks5h://u:p@h:1080", "scheme 大小写不敏感"),
    ("socks5h://u:p@h:1080", "socks5h://u:p@h:1080", "已是远端解析，原样"),
    ("socks4://h:1080", "socks4a://h:1080", "socks4 → socks4a"),
    ("socks4a://h:1080", "socks4a://h:1080", "已是 socks4a，原样"),
    ("http://u:p@h:3128", "http://u:p@h:3128", "http 代理不动"),
    ("  socks5://h:1080 \n", "socks5h://h:1080", "去首尾空白"),
]


def load_functions() -> dict:
    """只取注册机里的纯函数，避免导入模块时构造 opener/dataclass。"""
    src = REGISTRAR.read_text(encoding="utf-8")
    tree = ast.parse(src)
    keep = [n for n in tree.body
            if isinstance(n, ast.FunctionDef) and n.name in WANTED]
    missing = set(WANTED) - {n.name for n in keep}
    if missing:
        raise SystemExit(f"注册机缺少函数: {sorted(missing)}")
    ns: dict = {}
    exec(compile(ast.Module(body=keep, type_ignores=[]), str(REGISTRAR), "exec"), ns)
    return ns


def main() -> int:
    table = json.loads(CASES.read_text(encoding="utf-8"))
    fn = load_functions()
    failures: list[str] = []
    checked = 0

    for tc in table["normalize_uid"]:
        checked += 1
        got = fn["normalize_uid"](tc["in"])
        if got != tc["out"]:
            failures.append(
                f"normalize_uid({tc['in']!r}) = {got!r}, want {tc['out']!r}  [{tc['why']}]")

    for tc in table["session_value"]:
        checked += 1
        got = fn["session_value"](tc["in"])
        if got != tc["out"]:
            failures.append(
                f"session_value({tc['in']!r}) = {got!r}, want {tc['out']!r}  [{tc['why']}]")

    for raw, want, why in PROXY_CASES:
        checked += 1
        got = fn["remote_dns_proxy"](raw)
        if got != want:
            failures.append(f"remote_dns_proxy({raw!r}) = {got!r}, want {want!r}  [{why}]")

    if failures:
        print("FAIL: Python 与共享用例表不一致", file=sys.stderr)
        for f in failures:
            print("  -", f, file=sys.stderr)
        return 1

    print(f"ok: {checked} 条用例，Python 与 cases.json 一致")
    return 0


if __name__ == "__main__":
    sys.exit(main())
