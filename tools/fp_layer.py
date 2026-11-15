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

# --------------------------------------------------------------------------
# Chromium 版本 → 底座 的绑定表（2026-11-14 修正）
#
# 旧实现把 REAL_CHROME_157_JA3 无差别注入给全部 68 个 Chromium UA，
# 其中 56 个（82%）UA 声明版本 <=155。于是 UA 说 146、TLS 按 157 握手。
# 版本一致性是最容易被规则命中的特征。
#
# 另有一个更隐蔽的问题：curl_cffi 最高只到 chrome136，**不存在** 146-157 的
# 原生底座。所以"UA 146 + 注入 157 ja3"两边都不真实。
#
# 修正策略：UA 主版本与底座/ja3 成对出现。
#   - 底座 >= 143 可用（实测 chrome131/136 的 ja4 前缀均为 t13d1516h2）
#   - 更高版本的 UA 不再单独伪造，统一收敛到可用底座的真实版本段
#   - 宁可身份数少且自洽，也不要数量多但每一条都对不上
# --------------------------------------------------------------------------

# 实测（tls.peet.ws，2026-11-14）：
#   chrome131 → ja4 t13d1516h2_8daaf6152771_02713d6a  ja3 fb925f59992f46f50a85...
#   chrome136 → ja4 t13d1516h2_8daaf6152771_d8a2da3f  ja3 3261dd22d6f59ad2ca6c...
#   chrome133a→ ja4 t13d1516h2_8daaf6152771_d8a2da3f
#   chrome124 → ja4 t13d1516h2_8daaf6152771_02713d6a
# 实测（tls.peet.ws，2026-11-14）各底座的真实 ja4，仅作记录/审计用途：
#   chrome136 → ja4 t13d1516h2_8daaf6152771_d8a2da3f  ja3_hash 3261dd22d6f59ad2ca6c...
#   chrome131 → ja4 t13d1516h2_8daaf6152771_02713d6a  ja3_hash fb925f59992f46f50a85...
#   chrome124 → ja4 t13d1516h2_8daaf6152771_02713d6a  ja3_hash df1769dc43768a78453b...
#
# ⚠️ 注意：上面是 **ja3_hash**（MD5，32 位十六进制），不是 ja3 字符串。
# curl_cffi 的 ja3= 参数要求的是 5 段完整表达
#   "TLSVersion,Ciphers,Extensions,EllipticCurves,EllipticCurvePointFormats"
# 把 hash 塞进 ja3= 会抛 ValueError: not enough values to unpack (expected 5, got 1)。
# 因此这里只用底座提供 TLS 骨架，不手工注入 ja3 —— 底座本身就是同版本浏览器
# 的真实键值，版本自洽性由 clamp_chrome_ua() 保证。

CHROME_VERSION_BASES = [
    (130, "chrome136"),   # 当前 curl_cffi 可用的最高 Chromium 底座
    (120, "chrome131"),
    (114, "chrome124"),
    (0,   "chrome110"),
]

# curl_cffi（0.13.x，2026-11 实测）能提供的 Chromium 底座上限是 136。
# UA 池里 146-157 的 UA 没有对应底座；继续宣称未来版本只会让
# "UA 157 / TLS 136" 更明显。CHAFFREE_MAX_UA 以内的 UA 才是可自洽的。
CHROME_MAX_FAITHFUL = 136


def chrome_base_for(ua: str) -> str:
    """
    按 UA 里的 Chrome/Edg 主版本选底座（底座自带同版本的真实 TLS 骨架）。
    """
    import re as _re

    m = _re.search(r"(?:Chrome|Edg)/(\d+)", ua)
    if not m:
        return "chrome136"
    major = int(m.group(1))
    for threshold, base in CHROME_VERSION_BASES:
        if major >= threshold:
            return base
    return "chrome110"


def clamp_chrome_ua(ua: str, *, max_major: int = CHROME_MAX_FAITHFUL) -> str:
    """
    把 UA 里的 Chrome/Edg 版本收敛到有真实底座的范围内。

    只改版本号数字，保留平台段与构建号格式，避免破坏其余自洽性。
    """
    import re as _re

    m = _re.search(r"(Chrome|Edg)/(\d+)\.", ua)
    if not m:
        return ua
    major = int(m.group(2))
    if major <= max_major:
        return ua
    # 保留同一分支的个位，让分布不至于挤在一个点上
    new_major = max_major - (major % 3)
    return ua.replace(f"{m.group(1)}/{m.group(2)}.", f"{m.group(1)}/{new_major}.")


# --------------------------------------------------------------------------
# Firefox：curl_cffi 底座最高 firefox135（实测 UA 确认为 135.0）
# UA 池声明 139-148，与 TLS 相差 4-13 个版本 —— 同一类版本错位。
# --------------------------------------------------------------------------

FIREFOX_MAX_FAITHFUL = 135

# 可用的 Firefox 底座及其真实版本（2026-11-14 实测）
FIREFOX_BASES = [
    (133, "firefox133"),
    (135, "firefox135"),
]


def firefox_base_for(ua: str) -> str:
    """按 UA 里的 Firefox 主版本选最接近的可用底座。"""
    import re as _re

    m = _re.search(r"Firefox/(\d+)", ua)
    if not m:
        return "firefox135"
    major = int(m.group(1))
    best = "firefox135"
    for ver, base in FIREFOX_BASES:
        if major <= ver:
            return base
        best = base
    return best


def clamp_firefox_ua(ua: str, *, max_major: int = FIREFOX_MAX_FAITHFUL) -> str:
    """
    把 Firefox UA 的 rv:/Firefox/ 收敛到 <=135。

    Firefox 的 UA 有两处版本（rv: 和 Firefox/），必须同时改，否则自相矛盾。
    """
    import re as _re

    m = _re.search(r"rv:(\d+)\.", ua)
    f = _re.search(r"Firefox/(\d+)\.", ua)
    if not m and not f:
        return ua
    cur = int((m or f).group(1))
    if cur <= max_major:
        return ua
    new = max_major - (cur % 2)
    ua = _re.sub(r"rv:\d+\.", f"rv:{new}.", ua)
    ua = _re.sub(r"Firefox/\d+\.", f"Firefox/{new}.", ua)
    return ua


# --------------------------------------------------------------------------
# Safari：curl_cffi 有 safari153/155/170/180/184/260 六个底座，
# 已经能覆盖 UA 池里的 17/18/26 三个大版本，无需收敛。
# 但术语要统一：底座按大版本细分，避免一底座吃整个大版本。
# --------------------------------------------------------------------------

SAFARI_MAX_FAITHFUL = 26


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

    Chromium：先把 UA 版本收敛到 curl_cffi 有真实底座的区间
    （clamp_chrome_ua），再由底座提供同版本的完整 TLS 骨架。

    exact_chrome_ja3 保留为兼容参数，但不再用于注入手工 ja3 ——
    唯一可用的 ja3 源（Chrome 157）在 curl_cffi 里没有对应底座，
    且 ja3_hash 不能直接当 ja3 字符串用（见 CHROME_VERSION_BASES 注释）。

    ⚠️ 旧行为（已废弃，2026-11-14）：不分版本统一注入 REAL_CHROME_157_JA3，
    导致 82% 的 Chromium UA 出现 "UA 146 / TLS 157" 错位。
    """
    browser = entry["browser"]
    ua = entry.get("ua", "")
    imp = IMPERSONATE_BASE.get(browser, "chrome")
    if browser == "safari":
        imp = _safari_base(ua)
    ja3 = None
    akamai = None
    if browser in ("chrome", "edge"):
        ua = clamp_chrome_ua(ua)
        imp = chrome_base_for(ua)
    elif browser == "firefox":
        # UA 池声明 139-148，底座最高 135：先收敛再选最近底座
        ua = clamp_firefox_ua(ua)
        imp = firefox_base_for(ua)
    return FingerprintProfile(
        ua=ua,
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

    # 保存过的 ja3 若是完整 5 段表达才沿用；历史记录里存的是 ja3_hash
    # （32 位十六进制），不能直接喂给 curl_cffi，否则 ValueError。
    use_ja3 = None
    saved = (ja3 or "").strip()
    if saved and saved.count(",") == 4:
        use_ja3 = saved

    if browser in ("chrome", "edge"):
        # 与 build_profile 同一策略：UA 版本收敛 + 底座提供 TLS 骨架
        ua = clamp_chrome_ua(ua)
        imp = chrome_base_for(ua)

    # sec-ch-ua：Chromium 系必须有，Safari/Firefox 不能有
    sec_ch = None
    sec_plat = None
    if browser in ("chrome", "edge"):
        m = _re.search(r"(?:Chrome|Edg)/(\d+)", ua)
        major = m.group(1) if m else "136"
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
