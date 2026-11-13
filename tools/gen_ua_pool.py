#!/usr/bin/env python3
"""
生成 tools/data/ua_pool.json —— 真实浏览器 UA 池

数据依据：各浏览器真实 User-Agent 格式与发行版本号对应关系（截至 2026-11）。
  - Chrome : 每 4 周一个大版本；macOS 段为 Darwin/OS 版本（如 10_15_7 / 14_6_1）
  - Safari : macOS 段是 **macOS 系统版本**，Version/ 才是 Safari 版本，二者必须匹配
             （常见错误：把 Safari 版本写成 macOS 段，如 "Mac OS X 18_6" —— 不存在）
  - iOS    : iPhone OS 版本 = Safari 所在系统版本，Mobile/ 构建号对应系统
  - Edge   : 基于同一 Chromium 大版本
  - Firefox: rv: 与 Firefox/ 一致

用法：
  python3 tools/gen_ua_pool.py           # 重新生成
  python3 tools/gen_ua_pool.py --stats   # 只看统计
"""

from __future__ import annotations

import argparse
import json
from collections import Counter
from pathlib import Path

OUT = Path(__file__).resolve().parent / "data" / "ua_pool.json"

# --------------------------------------------------------------------------
# Chrome：版本 -> 完整 build
# --------------------------------------------------------------------------
CHROME_BUILD = {
    157: "8103.51", 156: "8078.80", 155: "8015.60", 154: "7942.82",
    153: "7872.70", 152: "7800.65", 151: "7728.55", 150: "7656.50",
    149: "7583.45", 148: "7510.40", 147: "7438.35", 146: "7365.30",
}
# macOS 系统版本（UA 段的写法），按真实 Darwin 版本
MAC_OS = ["10_15_7", "13_6_6", "14_6_1", "15_7_7"]
WIN_NT = ["10.0"]  # Win10/Win11 均写作 10.0；用 Win64; x64 区分位数


def _sec_ch_ua(major: int, brand: str = "chrome") -> str:
    if brand == "edge":
        return f'"Microsoft Edge";v="{major}", "Chromium";v="{major}", "Not.A/Brand";v="24"'
    return f'"Google Chrome";v="{major}", "Chromium";v="{major}", "Not.A/Brand";v="24"'


def chrome_entries() -> list[dict]:
    out = []
    for ver, build in CHROME_BUILD.items():
        full = f"{ver}.0.{build}"
        for osv in MAC_OS:
            out.append({
                "ua": (f"Mozilla/5.0 (Macintosh; Intel Mac OS X {osv}) AppleWebKit/537.36 "
                       f"(KHTML, like Gecko) Chrome/{full} Safari/537.36"),
                "platform": "macOS", "browser": "chrome", "mobile": False,
                "sec_ch_ua": _sec_ch_ua(ver), "sec_ch_ua_platform": '"macOS"',
            })
        for nt in WIN_NT:
            out.append({
                "ua": (f"Mozilla/5.0 (Windows NT {nt}; Win64; x64) AppleWebKit/537.36 "
                       f"(KHTML, like Gecko) Chrome/{full} Safari/537.36"),
                "platform": "Windows", "browser": "chrome", "mobile": False,
                "sec_ch_ua": _sec_ch_ua(ver), "sec_ch_ua_platform": '"Windows"',
            })
    return out


# --------------------------------------------------------------------------
# Safari macOS：macOS 系统版本 -> (Safari 版本, AppleWebKit 版本)
# 关键：Safari 随 macOS 系统发布，系统版本与 Safari 版本强绑定
# --------------------------------------------------------------------------
SAFARI_MAC = [
    # (macOS 系统版本, Safari 版本)
    ("15_7_7", "26.7"),   # macOS 15.7.x  → Safari 26.x
    ("15_6_2", "26.6"),
    ("15_5_1", "26.5"),
    ("15_4_1", "26.4"),
    ("15_3_1", "26.3"),
    ("15_2",   "26.2"),
    ("15_1_1", "26.1"),
    ("15_0_1", "26.0.1"),
    ("14_6_1", "18.6"),
    ("14_5",   "18.5"),
    ("14_4_1", "18.4"),
    ("14_3_1", "18.3"),
    ("14_2_1", "18.2"),
    ("14_1_1", "18.1"),
    ("14_0",   "18.0"),
    ("13_7_1", "18.6"),   # 老系统也能装新 Safari
    ("13_6_6", "18.6"),
    ("13_5_2", "17.6"),
    ("13_4_1", "17.5"),
    ("13_3_1", "17.4"),
    ("13_2_1", "17.3"),
    ("13_1_2", "17.2"),
    ("13_0_1", "17.1"),
    ("12_7_6", "17.6"),   # Safari 17 支持 macOS Monterey 起
    ("12_6_1", "17.6"),
]

# AppleWebKit 版本：Safari 26.x / 18.x / 17.x 对应不同 Whitenoise 版本
SAFARI_WEBKIT = {
    "26": "625.1.5",     # 示意：Safari 26 用 625.x
    "18": "620.1.16",
    "17": "619.2.55",
}


def safari_webkit(safari_ver: str) -> str:
    major = safari_ver.split(".")[0]
    return SAFARI_WEBKIT.get(major, "619.2.55")


def safari_mac_entries() -> list[dict]:
    out = []
    for osv, sver in SAFARI_MAC:
        wk = safari_webkit(sver)
        out.append({
            "ua": (f"Mozilla/5.0 (Macintosh; Intel Mac OS X {osv}) AppleWebKit/{wk} "
                   f"(KHTML, like Gecko) Version/{sver} Safari/{wk}"),
            "platform": "macOS", "browser": "safari", "mobile": False,
            "sec_ch_ua": None, "sec_ch_ua_platform": None,
        })
    return out


# --------------------------------------------------------------------------
# Safari iOS：iPhone OS 版本 -> Mobile 构建号
# --------------------------------------------------------------------------
IOS_BUILD = {
    "26_2": "23C55", "26_1": "23B85", "26_0_1": "23A8464", "26_0": "23A341",
    "18_6": "22G120", "18_5_1": "22F82", "18_5": "22F76", "18_4_1": "22E261",
    "18_4": "22E240", "18_3_1": "22D72", "18_3": "22D63", "18_2_1": "22C161",
    "18_2": "22C152", "18_1_1": "22B91", "18_1": "22B83", "18_0_1": "22A3370",
    "18_0": "22A3354", "17_7_1": "21H231", "17_7": "21H216", "17_6_1": "21G93",
    "17_6": "21G80", "17_5_1": "21F90", "17_5": "21F79", "17_4_1": "21E236",
}


def safari_ios_entries() -> list[dict]:
    out = []
    for osv, build in IOS_BUILD.items():
        ver = osv.replace("_", ".")
        # iOS Safari 版本基本跟系统版本走（26.x 时 Safari 版本号 = 26.x）
        safari_ver = ver
        wk = safari_webkit(ver.split(".")[0])
        out.append({
            "ua": (f"Mozilla/5.0 (iPhone; CPU iPhone OS {osv} like Mac OS X) "
                   f"AppleWebKit/{wk} (KHTML, like Gecko) Version/{safari_ver} "
                   f"Mobile/{build} Safari/604.1"),
            "platform": "iOS", "browser": "safari", "mobile": True,
            "sec_ch_ua": None, "sec_ch_ua_platform": None,
        })
    return out


# --------------------------------------------------------------------------
# Edge / Firefox
# --------------------------------------------------------------------------
EDGE_BUILD = {
    157: "3405.84", 156: "3345.79", 155: "3277.65", 154: "3197.60",
    153: "3128.55", 152: "3063.50", 151: "2987.45", 150: "2912.40",
}


def edge_entries() -> list[dict]:
    out = []
    for ver, build in EDGE_BUILD.items():
        full = f"{ver}.0.{build}"
        out.append({
            "ua": (f"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
                   f"(KHTML, like Gecko) Chrome/{full} Safari/537.36 Edg/{full}"),
            "platform": "Windows", "browser": "edge", "mobile": False,
            "sec_ch_ua": _sec_ch_ua(ver, "edge"), "sec_ch_ua_platform": '"Windows"',
        })
    return out


FIREFOX_VER = [
    "148.0", "147.0.3", "146.0.2", "145.0.1", "144.0",
    "143.0.4", "142.0.1", "141.0", "140.0.2", "139.0",
]


def firefox_entries() -> list[dict]:
    out = []
    for v in FIREFOX_VER:
        out.append({
            "ua": f"Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:{v}) Gecko/20100101 Firefox/{v}",
            "platform": "macOS", "browser": "firefox", "mobile": False,
            "sec_ch_ua": None, "sec_ch_ua_platform": None,
        })
        out.append({
            "ua": f"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:{v}) Gecko/20100101 Firefox/{v}",
            "platform": "Windows", "browser": "firefox", "mobile": False,
            "sec_ch_ua": None, "sec_ch_ua_platform": None,
        })
    return out


def build_pool() -> list[dict]:
    pool: list[dict] = []
    pool += chrome_entries()
    pool += safari_mac_entries()
    pool += safari_ios_entries()     # iOS 条目保留 mobile=True，分配时默认排除
    pool += edge_entries()
    pool += firefox_entries()
    # 去重（保序）
    seen, uniq = set(), []
    for e in pool:
        if e["ua"] in seen:
            continue
        seen.add(e["ua"])
        uniq.append(e)
    return uniq


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--stats", action="store_true")
    args = ap.parse_args()

    pool = build_pool()
    if not args.stats:
        OUT.parent.mkdir(parents=True, exist_ok=True)
        OUT.write_text(json.dumps(pool, ensure_ascii=False, indent=1), encoding="utf-8")

    print(f"总计 {len(pool)} 条  ->  {OUT}")
    print("平台:", dict(Counter(p["platform"] for p in pool)))
    print("浏览器:", dict(Counter(p["browser"] for p in pool)))
    print("非移动端:", sum(1 for p in pool if not p["mobile"]))
    print("Safari 明细:", dict(Counter(p["platform"] for p in pool if p["browser"] == "safari")))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
