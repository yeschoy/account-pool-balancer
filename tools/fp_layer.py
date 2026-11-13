#!/usr/bin/env python3
"""
deploy.com 注册机 —— 网络指纹层（UA 池 + TLS/H2 指纹 + 代理）

设计目标（2026-11-13 实测驱动）：
  1. 106 个真实浏览器 UA，**每个账号固定绑定一个**，不逐请求轮换
  2. UA 与 TLS 指纹必须"同一浏览器同一版本"自洽，否则比不伪装更可疑
  3. 优先用 curl_cffi 的 ja3= 精确注入本机真 Chrome 测得的指纹
  4. 代理池透传（用户自备），每账号可绑定固定出口

实测基准（本机 Chrome 157 headless 访问 tls.peet.ws）：
  ja3_hash : 656aab6b92a69987dc81f14b09ccc8c1
  ja4      : t13d1517h2_8daaf6152771_cb7bf5808d99
  h2 akamai: 52d84b11737d980aef856699f885ca86

curl_cffi 实测能力边界：
  - 可精确控制：cipher 列表、扩展**存在性**、h2 settings/优先级、gzip 等
  - 不可控制  ：扩展 51764（Chrome 私有扩展）→ 无法 100% 复刻 Chrome 157 的 ja3
  - 最佳可达  ：ja4 = t13d1517h2_8daaf6152771_fca9c764716e（前缀与真 Chrome 完全一致）
"""

from __future__ import annotations

import json
import os
import random
from dataclasses import dataclass, asdict
from pathlib import Path
from typing import Any

DATA_DIR = Path(__file__).resolve().parent / "data"
UA_POOL_PATH = DATA_DIR / "ua_pool.json"

# --------------------------------------------------------------------------
# 真 Chrome 157 指纹常量（实测，见文件头）
# --------------------------------------------------------------------------

REAL_CHROME_157_JA3 = (
    "771,4865-4866-4867-49195-49199-49196-49200-52393-52392-49171-49172-156-157-47-53,"
    # 扩展顺序照抄真 Chrome；51764 无法被 curl_cffi 切换，已剔除
    "51-0-45-17613-13-27-65281-11-5-16-18-10-65037-35-43-23,"
    "4588-29-23-24,0"
)
REAL_CHROME_H2 = "1:65536;2:0;4:6291456;6:262144|15663105|0|m,a,s,p"

# 每种浏览器在 curl_cffi 里的"底座"impersonate
# 底座负责提供该浏览器的协议骨架；UA 里的版本号只是文本，不必与底座版本一致，
# 但**同浏览器大类必须一致**（不能用 Firefox UA 配 Chrome TLS）。
IMPERSONATE_BASE = {
    "chrome": "chrome",
    "edge": "chrome",       # Edge 是 Chromium，TLS 与 Chrome 同族
    "safari": "safari17_0",
    "firefox": "firefox133",
}

# Safari 按大版本号细分底座（实测可用：safari15_5 / safari17_0 / safari18_0 / safari260）
# 不同 Safari 大版本 TLS 扩展哈希不同，细分后 UA 与指纹版本才自洽。
#
# ⚠️ 命名规则不统一，写错就是请求直接失败（impersonate 无效 → ImpersonateError，
#    注册机连不上 deploy.com，整批账号被判失败）：
#      safari15_5 / safari17_0 / safari18_0   带下划线
#      safari260                              **不带**下划线（2026-11-13 在
#                                             curl_cffi 0.17.0 上实测确认）
#    历史上这里写成 "safari26_0"，导致 Safari 26 的 UA 全部无法出网。
SAFARI_BASES = [
    (26, "safari260"),
    (18, "safari18_0"),
    (17, "safari17_0"),
    (0,  "safari15_5"),
]


def _safari_base(ua: str) -> str:
    """从 Safari UA 的 Version/xx.y 取大版本，选对应底座。"""
    import re as _re

    m = _re.search(r"Version/(\d+)", ua)
    if not m:
        return "safari17_0"
    major = int(m.group(1))
    for threshold, base in SAFARI_BASES:
        if major >= threshold:
            return base
    return "safari15_5"


@dataclass
class FingerprintProfile:
    """一个账号完整绑定的网络身份。"""
    ua: str
    platform: str
    browser: str
    mobile: bool
    sec_ch_ua: str | None
    sec_ch_ua_platform: str | None
    impersonate: str
    ja3: str | None
    akamai: str | None

    def headers(self) -> dict[str, str]:
        """按 UA 自动补齐配套头，保证 UA 与 sec-ch-ua 版本号自洽。"""
        h = {"User-Agent": self.ua}
        if self.sec_ch_ua:
            h["sec-ch-ua"] = self.sec_ch_ua
        if self.sec_ch_ua_platform:
            h["sec-ch-ua-platform"] = self.sec_ch_ua_platform
            h["sec-ch-ua-mobile"] = "?1" if self.mobile else "?0"
        if self.browser in ("chrome", "edge"):
            h["Accept-Language"] = random.choice(
                ["en-US,en;q=0.9", "en-US,en;q=0.9,zh-CN;q=0.8", "en-GB,en;q=0.9", "en-US,en;q=0.8"]
            )
        return h

    def request_kwargs(self) -> dict[str, Any]:
        """
        给 curl_cffi 的请求参数。

        ⚠️ 实测：impersonate 会覆盖 ja3（同时传时 ja4 退回 t13d1516）。
        所以注入 ja3 时不传 impersonate；不注入时用底座 impersonate。
        """
        kw: dict[str, Any] = {"headers": self.headers()}
        if self.ja3:
            kw["ja3"] = self.ja3
            if self.akamai:
                kw["akamai"] = self.akamai
        else:
            kw["impersonate"] = self.impersonate
        return kw

    def as_dict(self) -> dict[str, Any]:
        return asdict(self)


def load_ua_pool() -> list[dict]:
    if not UA_POOL_PATH.exists():
        raise FileNotFoundError(f"UA 池缺失: {UA_POOL_PATH}，请先运行 gen_ua_pool.py")
    return json.loads(UA_POOL_PATH.read_text(encoding="utf-8"))


def build_profile(entry: dict, *, exact_chrome_ja3: bool = True) -> FingerprintProfile:
    """
    由 UA 池条目构造完整指纹档案。

    exact_chrome_ja3: Chrome/Edge 是否注入实测的真 Chrome JA3。
      True  → ja4 前缀 t13d1517（与真 Chrome 一致），但缺 51764
      False → 用 curl_cffi 内置 chrome 底座（t13d1516），版本更"老"但完全自洽
    """
    browser = entry["browser"]
    imp = IMPERSONATE_BASE.get(browser, "chrome")
    if browser == "safari":
        imp = _safari_base(entry.get("ua", ""))
    ja3 = None
    akamai = None
    if exact_chrome_ja3 and browser in ("chrome", "edge"):
        ja3 = REAL_CHROME_157_JA3
        akamai = REAL_CHROME_H2
    return FingerprintProfile(
        ua=entry["ua"],
        platform=entry["platform"],
        browser=browser,
        mobile=entry.get("mobile", False),
        sec_ch_ua=entry.get("sec_ch_ua"),
        sec_ch_ua_platform=entry.get("sec_ch_ua_platform"),
        impersonate=imp,
        ja3=ja3,
        akamai=akamai,
    )


def assign_profiles(
    count: int,
    *,
    exclude_mobile: bool = True,
    seed: int | None = None,
    exact_chrome_ja3: bool = True,
) -> list[FingerprintProfile]:
    rng = random.Random(seed)
    pool = load_ua_pool()
    if exclude_mobile:
        pool = [p for p in pool if not p.get("mobile")]
    if not pool:
        raise ValueError("UA 池为空")
    rng.shuffle(pool)
    out: list[FingerprintProfile] = []
    for i in range(count):
        entry = pool[i % len(pool)]
        out.append(build_profile(entry, exact_chrome_ja3=exact_chrome_ja3))
    return out


# --------------------------------------------------------------------------
# 代理池
# --------------------------------------------------------------------------

def load_proxies(path: str | None = None) -> list[str]:
    """
    代理池文件：每行一个，支持
      http://user:pass@host:port
      socks5://user:pass@host:port
      host:port            （视为 http）
    也接受环境变量 DEPLOY_PROXY_POOL，用逗号分隔。
    """
    raw = ""
    if path and Path(path).exists():
        raw = Path(path).read_text(encoding="utf-8")
    elif os.environ.get("DEPLOY_PROXY_POOL"):
        raw = os.environ["DEPLOY_PROXY_POOL"].replace(",", "\n")
    out = []
    for line in raw.splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        if "://" not in line:
            line = "http://" + line
        out.append(line)
    return out


def proxy_for_account(proxies: list[str], index: int) -> str | None:
    """每账号固定一个代理，避免同一出口被多号复用。"""
    if not proxies:
        return None
    return proxies[index % len(proxies)]


# --------------------------------------------------------------------------
# 会话复用：从已保存的账号记录还原指纹
# --------------------------------------------------------------------------

def restore_profile(
    *,
    ua: str = "",
    browser: str = "",
    platform: str = "",
    ja3: str = "",
    exact_chrome_ja3: bool = True,
) -> FingerprintProfile | None:
    """
    用账号首次注册时保存的 UA / 浏览器 / ja3 还原指纹档案。

    会话复用（登录、绑卡、查余额）**必须**沿用注册时的指纹：
    UA 与 TLS 对不上的请求，比没有指纹更容易被识别为账号盗用。

    ja3 为空时（Firefox 走 impersonate 底座）按浏览器大类选底座，
    保证 TLS 与 UA 至少同族。
    """
    import re as _re

    ua = (ua or "").strip()
    if not ua:
        return None
    low = ua.lower()
    browser = (browser or "").strip().lower()
    if not browser:
        if "edg/" in low:
            browser = "edge"
        elif "firefox/" in low:
            browser = "firefox"
        elif "chrome/" in low:
            browser = "chrome"
        elif "safari/" in low:
            browser = "safari"

    imp = IMPERSONATE_BASE.get(browser, "chrome")
    if browser == "safari":
        imp = _safari_base(ua)

    # 优先用保存的 ja3；没有则 Chrome/Edge 回落到实测基准
    use_ja3 = ja3.strip() if ja3 else None
    if not use_ja3 and exact_chrome_ja3 and browser in ("chrome", "edge"):
        use_ja3 = REAL_CHROME_157_JA3

    # sec-ch-ua：Chromium 系必须有，Safari/Firefox 不能有
    sec_ch = None
    sec_plat = None
    if browser in ("chrome", "edge"):
        m = _re.search(r"(?:Chrome|Edg)/(\d+)", ua)
        major = m.group(1) if m else "157"
        if browser == "edge":
            sec_ch = f'"Microsoft Edge";v="{major}", "Chromium";v="{major}", "Not.A/Brand";v="24"'
        else:
            sec_ch = f'"Google Chrome";v="{major}", "Chromium";v="{major}", "Not.A/Brand";v="24"'
        if "Windows" in ua:
            sec_plat = '"Windows"'
        elif "Macintosh" in ua:
            sec_plat = '"macOS"'
        elif "Linux" in ua:
            sec_plat = '"Linux"'

    plat = platform or (
        "Windows" if "Windows" in ua else
        "macOS" if "Macintosh" in ua else
        "iOS" if "iPhone" in ua else
        "Linux" if "Linux" in ua else ""
    )
    return FingerprintProfile(
        ua=ua,
        platform=plat,
        browser=browser,
        mobile=("iPhone" in ua or "Mobile/" in ua),
        sec_ch_ua=sec_ch,
        sec_ch_ua_platform=sec_plat,
        impersonate=imp,
        ja3=use_ja3,
        akamai=REAL_CHROME_H2 if use_ja3 else None,
    )
