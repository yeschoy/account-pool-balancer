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
WANTED = ("normalize_uid", "session_value")


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

    if failures:
        print("FAIL: Python 与共享用例表不一致", file=sys.stderr)
        for f in failures:
            print("  -", f, file=sys.stderr)
        return 1

    print(f"ok: {checked} 条用例，Python 与 cases.json 一致")
    return 0


if __name__ == "__main__":
    sys.exit(main())
