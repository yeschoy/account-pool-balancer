"""
代理出口 IP 去重测试（一 IP 一号）

背景：旧实现是 by_index(n) = proxies[n % len]，10 个代理 40 个号 =>
每个住宅 IP 4 个号，串行连续跑，IP 信誉直接打穿。
现在 find_working_proxy 支持 exclude_ips，worker 会把已用过的出口 IP 传进来。

另外回归：健康检查不能再打目标站。
"""
from __future__ import annotations

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent / "signup"))

import core.chain_gateway as cg  # noqa: E402
from core.proxy_pool import Proxy, ProxyPool  # noqa: E402


def _pool(n: int) -> ProxyPool:
    p = ProxyPool(["socks5://127.0.0.1:%d" % (20000 + i) for i in range(n)])
    return p


def _fake_probe(mapping):
    """按代理端口返回不同的出口 IP"""
    def probe(local_socks, timeout=25):
        port = int(local_socks.rsplit(":", 1)[-1])
        # GatewayPool 的本地端口和上游端口不同，用序号近似映射
        idx = port % len(mapping)
        ip, country = mapping[idx]
        return {"ok": True, "ip": ip, "country": country, "colo": "YYZ",
                "raw": "", "error": ""}
    return probe


def test_skips_excluded_exit_ip():
    pool = _pool(3)
    gw = cg.GatewayPool()
    orig = cg.probe_exit
    try:
        # 让每个代理拿到固定 IP
        cg.probe_exit = _fake_probe([("1.1.1.1", "CA"), ("2.2.2.2", "CA"), ("3.3.3.3", "CA")])
        # 先探一遍，确认能拿到 IP
        first = cg.probe_exit(gw.get(pool.all()[0].url))
        assert first["ip"], first

        # 排除掉所有能拿到的 IP，应该返回 None
        all_ips = ["1.1.1.1", "2.2.2.2", "3.3.3.3"]
        px, local, geo = cg.find_working_proxy(pool, gw, exclude_ips=all_ips, max_tries=6)
        assert px is None, f"所有出口 IP 都被占用时不该返回代理，实际 {px}"
        assert geo == {}
    finally:
        cg.probe_exit = orig
        gw.close_all()


def test_returns_proxy_when_not_excluded():
    pool = _pool(3)
    gw = cg.GatewayPool()
    orig = cg.probe_exit
    try:
        cg.probe_exit = _fake_probe([("1.1.1.1", "CA"), ("2.2.2.2", "CA"), ("3.3.3.3", "CA")])
        px, local, geo = cg.find_working_proxy(pool, gw, exclude_ips=[], max_tries=6)
        assert px is not None, "有空闲 IP 时必须返回代理"
        assert geo["ip"], geo
        assert local.startswith("socks5://127.0.0.1:")
    finally:
        cg.probe_exit = orig
        gw.close_all()


def test_probe_never_targets_mistral():
    """健康检查绝不能碰目标站：那是"先 curl 后注册"的教科书指纹。

    注释/文档字符串里提到旧实现是允许的，只检查真正的字符串字面量。
    """
    import ast
    src = Path(cg.__file__).read_text()
    tree = ast.parse(src)

    docstrings = set()
    for node in ast.walk(tree):
        if isinstance(node, (ast.Module, ast.FunctionDef,
                             ast.AsyncFunctionDef, ast.ClassDef)):
            d = ast.get_docstring(node, clean=False)
            if d is not None:
                docstrings.add(d)

    bad = [
        n.value for n in ast.walk(tree)
        if isinstance(n, ast.Constant) and isinstance(n.value, str)
        and "mistral.ai" in n.value and n.value not in docstrings
    ]
    assert not bad, f"chain_gateway 里还有对目标站的字符串字面量: {bad}"
    assert "cloudflare.com/cdn-cgi/trace" in src, "出口探测必须走第三方回显"


def test_rotated_is_deterministic():
    pool = _pool(4)
    a = [p.sid for p in pool.rotated(0)]
    b = [p.sid for p in pool.rotated(0)]
    assert a == b, "rotated() 必须确定性，否则无法复现"
    c = [p.sid for p in pool.rotated(1)]
    assert c[0] == a[1], "rotated(1) 必须从第二个开始"


if __name__ == "__main__":
    failed = 0
    for name, fn in sorted(globals().items()):
        if not name.startswith("test_") or not callable(fn):
            continue
        try:
            fn()
            print(f"  ok   {name}")
        except AssertionError as e:
            failed += 1
            print(f"  FAIL {name}: {e}")
    print(f"{failed} failed")
    sys.exit(1 if failed else 0)
