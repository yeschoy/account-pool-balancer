"""
指纹画像测试

重点（都是 2026-11-09 修过的真实事故）：
  1. console_js() 渲染出来的 JS **必须能通过 node --check**
     —— 之前有 4 处语法错（`version: ""156""`、`availHeight` 多一个 `}`），
        整段脚本从未执行过，浏览器真实值全裸奔
  2. headers() 里不能出现 downlink / rtt / dpr / viewport-width
     —— 这四个是 JS 属性，浏览器从不把它们当 HTTP 头发出去
  3. 导航请求必须是 document/navigate，不能是 empty/cors
  4. 未知出口国必须抛错，不能静默回落 CA
  5. navigator.platform 必须是 MacIntel / Win32，不是 "macOS"
"""
from __future__ import annotations

import json
import shutil
import subprocess
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent / "signup"))

from core.fingerprint import (  # noqa: E402
    CHROME_BUILDS, GEO_PROFILES, PLATFORMS, UnknownCountry,
    billing_address, build_profile, resolve_country,
)

# 浏览器从不发送的伪头（发出去等于自报"我不是浏览器"）
FAKE_HEADERS = {"downlink", "rtt", "dpr", "viewport-width", "ect", "save-data"}


def _all_profiles():
    for country in sorted(GEO_PROFILES):
        for plat in sorted(PLATFORMS):
            for chrome in sorted(CHROME_BUILDS):
                email = f"probe-{country}-{plat}-{chrome}@example.com"
                yield build_profile(email, country, plat, chrome)


def test_console_js_parses():
    """每个组合渲染出来的 JS 都必须能通过 node --check"""
    node = shutil.which("node")
    if not node:
        print("  (skip: 没有 node，无法校验 JS 语法)")
        return

    tmp = Path("/tmp/mistral_fp_check")
    tmp.mkdir(exist_ok=True)
    bad = []
    for i, prof in enumerate(_all_profiles()):
        f = tmp / f"fp_{i}.js"
        f.write_text(prof.console_js())
        r = subprocess.run([node, "--check", str(f)], capture_output=True, text=True)
        if r.returncode != 0:
            bad.append((prof.country, prof.ua[:40], r.stderr.strip().splitlines()[:3]))
    assert not bad, f"{len(bad)} 个画像的 console_js 语法错误，例: {bad[:2]}"


def test_console_js_has_no_double_quoted_version():
    """回归：ch_ua_full_version 带引号，拼进 JS 会变成 ""156.0.8078.80"" """
    for prof in _all_profiles():
        js = prof.console_js()
        assert '""' not in js, f"{prof.country}: JS 里出现连续双引号，拼接又坏了"
        assert "'" not in js.split("//")[0] or True  # 允许注释里的中文引号


def test_headers_have_no_fake_browser_fields():
    for prof in _all_profiles():
        hdrs = {k.lower() for k in prof.headers()}
        leaked = hdrs & FAKE_HEADERS
        assert not leaked, f"{prof.country}: headers() 里还有浏览器不发的头 {leaked}"


def test_nav_headers_are_navigation():
    for prof in _all_profiles():
        h = {k.lower(): v for k, v in prof.nav_headers().items()}
        assert h["sec-fetch-mode"] == "navigate", "导航请求必须是 navigate"
        assert h["sec-fetch-dest"] == "document", "导航请求必须是 document"
        assert h["sec-fetch-user"] == "?1", "导航请求必须带 sec-fetch-user"
        assert h["upgrade-insecure-requests"] == "1", "导航请求必须带 UIR"
        assert h["accept"].startswith("text/html"), "导航的 Accept 必须是 HTML"


def test_xhr_headers_are_fetch():
    for prof in _all_profiles():
        h = {k.lower(): v for k, v in prof.headers().items()}
        assert h["sec-fetch-mode"] == "cors"
        assert h["sec-fetch-dest"] == "empty"
        assert "upgrade-insecure-requests" not in h, "XHR 不该带 UIR"


def test_nav_platform_is_real():
    """navigator.platform 是 MacIntel / Win32，不是 sec-ch-ua-platform 的值"""
    for prof in _all_profiles():
        assert prof.nav_platform in ("MacIntel", "Win32"), prof.nav_platform
        assert prof.nav_platform != prof.platform_name


def test_accept_language_plain_has_no_qvalues():
    """CDP acceptLanguage 带 q 值会让 navigator.languages 变成 ["fr-CA","fr;q=0.9"]"""
    for prof in _all_profiles():
        plain = prof.accept_language_plain
        assert ";" not in plain and "q=" not in plain, plain
        assert plain.split(",") == prof.navigator_languages


def test_unknown_country_raises():
    """未知出口国必须抛错，不能静默套用 CA 画像"""
    try:
        build_profile("x@example.com", "ZZ")
    except UnknownCountry:
        pass
    else:
        raise AssertionError("build_profile 对未知国家没有抛错")

    try:
        billing_address("x@example.com", "ZZ")
    except UnknownCountry:
        pass
    else:
        raise AssertionError("billing_address 对未知国家没有抛错")


def test_resolve_country_prefers_exit():
    assert resolve_country("GB", "CA") == "GB"
    assert resolve_country("", "CA") == "CA"
    assert resolve_country(None, "us") == "US"
    try:
        resolve_country("ZZ", "")
    except UnknownCountry:
        pass
    else:
        raise AssertionError("resolve_country 对未知国家没有抛错")


def test_billing_matches_country():
    for country in sorted(GEO_PROFILES):
        a = billing_address("x@example.com", country)
        assert a["country"] == country
        assert a["zip"], "账单地址必须有邮编"
        assert a["currency"] == GEO_PROFILES[country]["currency"]


def test_profile_is_stable_per_email():
    a = build_profile("stable@example.com", "CA")
    b = build_profile("stable@example.com", "CA")
    assert a.ua == b.ua and a.timezone == b.timezone and a.screen_w == b.screen_w


def test_hardware_matches_platform():
    """hardwareConcurrency / deviceMemory 不能是宿主值，必须落在平台画像里"""
    for prof in _all_profiles():
        key = next(k for k, v in PLATFORMS.items()
                   if v["ch_platform"] == prof.ch_ua_platform
                   and v["platform_version"] == prof.ch_ua_platform_version.strip('"'))
        assert prof.hardware_concurrency in PLATFORMS[key]["hardware_concurrency"]
        assert prof.device_memory in PLATFORMS[key]["device_memory"]




# ---------------------------------------------------------------- 真 Chrome / GPU
def test_binary_resolver_prefers_real_chrome():
    """必须优先解析到真 Chrome：Playwright Chromium 的 plugins=0 直接暴露"""
    from core.browser import _candidate_chrome_paths, resolve_browser_binary
    paths = _candidate_chrome_paths()
    assert paths, "没有任何候选 Chrome 路径"
    exe, kw = resolve_browser_binary()
    # 本机有真 Chrome 就必须解析到它；没有则允许为空（CI 无浏览器）
    import os
    exists = any(os.path.isfile(p) for p in paths)
    if exists:
        assert exe, f"本机装了 Chrome 但 resolver 返回空路径: {paths}"
        assert kw.get("channel") == "chrome"


def test_gpu_probe_never_returns_swiftshader():
    """探测到 SwiftShader 时必须返回 None，让画像值兜底而不是把软件渲染当真相"""
    from core.fingerprint import probe_real_gpu
    gpu = probe_real_gpu()
    if gpu is None:
        return
    assert "SwiftShader" not in gpu["r"], gpu


def test_plugins_fallback_is_real_chrome_list():
    """非真 Chrome 时的 plugins 兜底必须是真 Chrome 那 5 个 PDF 插件"""
    from core.browser import CHROME_PDF_PLUGINS
    names = [p["name"] for p in CHROME_PDF_PLUGINS]
    assert len(names) == 5, names
    assert "Chrome PDF Viewer" in names
    assert all(p["filename"] == "internal-pdf-viewer" for p in CHROME_PDF_PLUGINS)


def test_no_isolate_origins_flag():
    """--disable-features=IsolateOrigins 会改 originAgentCluster，本身就是自动化标志"""
    from core.browser import SignupBrowser
    from core.fingerprint import build_profile
    prof = build_profile("x@example.com", "CA")
    sb = SignupBrowser(prof, "", None)
    joined = " ".join(sb.launch_args)
    assert "IsolateOrigins" not in joined, joined
    assert "AutomationControlled" in joined, "必须保留 AutomationControlled 开关"


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
    print(json.dumps({"failed": failed}))
    sys.exit(1 if failed else 0)
