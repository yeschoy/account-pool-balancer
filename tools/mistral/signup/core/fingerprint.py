"""
设备画像：每个账号绑定一套**内部完全自洽**的指纹。

设计原则（来自抓包分析）：
  1. UA + 12 个 Client Hints 必须整体匹配，不能只改 UA
  2. 语言 / 时区 / 地址 必须与代理出口国一致
  3. Canvas / WebGL / Audio 指纹在账号内固定，账号间不同

矛盾点：既要"随机 UA"避免批量特征，又要"固定指纹"显得像同一真人。
解法：账号级固定画像，账号间随机。

⚠️ 2026-11-09 修正（见 FINGERPRINT_FIXES.md）：
  - console_js() 之前有 4 处语法错，整段脚本从未执行过（浏览器真实值全裸奔）
  - headers() 之前会发 downlink / rtt / dpr / viewport-width 四个浏览器从不发的头
  - 国家不再静默回落 CA：未知国家直接抛错，避免"代理在美国、画像在加拿大"
"""
from __future__ import annotations

import hashlib
import json
import os
import random
from dataclasses import dataclass, field


# Chrome 版本 -> 一轮完整的 Client Hints 组合
# 注意: Not:A-Brand 的版本号随 Chrome 大版本变化，不能硬编码
CHROME_BUILDS = {
    "156": {
        "major": "156",
        "full": "156.0.8078.80",
        "brand_version": "8",      # Not:A-Brand 的 v=
        "brand_full": "8.0.0.0",
    },
    "155": {
        "major": "155",
        "full": "155.0.7981.65",
        "brand_version": "8",
        "brand_full": "8.0.0.0",
    },
}

# 平台画像：每个平台的 UA、sec-ch-ua-platform、platform-version、arch、bitness 必须成套
#
# nav_platform 是 navigator.platform 的值，和 sec-ch-ua-platform 不是一回事：
#   macOS  -> "MacIntel"
#   Windows-> "Win32"
# 之前用 sec-ch-ua-platform 去掉引号当 navigator.platform，等于自报 "macOS"，
# 而真 Chrome 报的是 "MacIntel"。
PLATFORMS = {
    "macos-arm": {
        "ua_platform": "(Macintosh; Intel Mac OS X 10_15_7)",
        "ch_platform": '"macOS"',
        "nav_platform": "MacIntel",
        "platform_version": "15.7.7",
        "arch": '"arm"',
        "bitness": '"64"',
        "wow64": "?0",
        "menu_bar_px": 25,
        "webgl_vendor": "Google Inc. (Apple)",
        "webgl_renderer": "ANGLE (Apple, ANGLE Metal Renderer: Apple M3, Unspecified Version)",
        "hardware_concurrency": [8, 10, 12],
        "device_memory": [8],
    },
    "macos-intel": {
        # Intel Mac 最高只到 macOS 15.x 的特定版本，且 M 系列才会报 arm
        # 这里用一组真实存在的 Intel Mac 组合，避免与 arm 画像混淆
        "ua_platform": "(Macintosh; Intel Mac OS X 10_15_7)",
        "ch_platform": '"macOS"',
        "nav_platform": "MacIntel",
        "platform_version": "15.3.1",
        "arch": '"x86"',
        "bitness": '"64"',
        "wow64": "?0",
        "menu_bar_px": 25,
        # ANGLE renderer 的前缀必须与 vendor 括号里的厂商标识一致（Intel Inc.）
        "webgl_vendor": "Google Inc. (Intel Inc.)",
        "webgl_renderer": "ANGLE (Intel Inc., Intel(R) Iris(TM) Plus Graphics 640, OpenGL 4.1)",
        "hardware_concurrency": [4, 8],
        "device_memory": [8, 16],
    },
    "windows": {
        "ua_platform": "(Windows NT 10.0; Win64; x64)",
        "ch_platform": '"Windows"',
        "nav_platform": "Win32",
        "platform_version": "15.0.0",
        "arch": '"x86"',
        "bitness": '"64"',
        "wow64": "?0",
        "menu_bar_px": 40,
        "webgl_vendor": "Google Inc. (NVIDIA)",
        "webgl_renderer": "ANGLE (NVIDIA, NVIDIA GeForce RTX 3060 (0x00002504) Direct3D11 vs_5_0 ps_5_0, D3D11)",
        "hardware_concurrency": [8, 12, 16],
        "device_memory": [8],
    },
}

# 出口国 -> 语言/时区/地址 画像
# 关键: 语言和时区必须与代理出口国一致（抓包里 CA 出口 + zh-CN 是致命破绽）
#
# 只列支持的国家。不在表里的国家会在 build_profile() 抛错，
# 而不是像以前那样静默套用加拿大画像（那等于 100% 地理矛盾）。
GEO_PROFILES = {
    "CA": {
        "languages": [
            ("en-CA,en;q=0.9", ["en-CA", "en"], "en-CA"),
            ("en-US,en;q=0.9", ["en-US", "en"], "en-US"),
            ("fr-CA,fr;q=0.9,en;q=0.8", ["fr-CA", "fr", "en"], "fr-CA"),
        ],
        "timezones": ["America/Toronto", "America/Vancouver", "America/Edmonton", "America/Winnipeg"],
        "locale": "en-CA",
        "currency": "CAD",
        "addresses": [
            {"line1": "1200 rue Sainte-Catherine O", "city": "Montreal", "state": "Quebec", "state_code": "QC", "zip": "H3B 1K1"},
            {"line1": "88 Queen St W", "city": "Toronto", "state": "Ontario", "state_code": "ON", "zip": "M5H 2M5"},
            {"line1": "1055 W Georgia St", "city": "Vancouver", "state": "British Columbia", "state_code": "BC", "zip": "V6E 3P3"},
        ],
    },
    "US": {
        "languages": [
            ("en-US,en;q=0.9", ["en-US", "en"], "en-US"),
            ("en-GB,en;q=0.9,en-US;q=0.8", ["en-GB", "en", "en-US"], "en-GB"),
        ],
        "timezones": ["America/New_York", "America/Chicago", "America/Denver", "America/Los_Angeles"],
        "locale": "en-US",
        "currency": "USD",
        "addresses": [
            {"line1": "350 Fifth Avenue", "city": "New York", "state": "New York", "state_code": "NY", "zip": "10118"},
            {"line1": "233 S Wacker Dr", "city": "Chicago", "state": "Illinois", "state_code": "IL", "zip": "60606"},
            {"line1": "1 Market St", "city": "San Francisco", "state": "California", "state_code": "CA", "zip": "94105"},
        ],
    },
    "GB": {
        "languages": [
            ("en-GB,en;q=0.9", ["en-GB", "en"], "en-GB"),
            ("en-GB,en;q=0.9,en-US;q=0.8", ["en-GB", "en", "en-US"], "en-GB"),
        ],
        "timezones": ["Europe/London"],
        "locale": "en-GB",
        "currency": "GBP",
        "addresses": [
            {"line1": "14 Deansgate", "city": "Manchester", "state": "England", "state_code": "ENG", "zip": "M3 2BW"},
            {"line1": "45 Corn Street", "city": "Bristol", "state": "England", "state_code": "ENG", "zip": "BS1 1HT"},
        ],
    },
    "DE": {
        "languages": [
            ("de-DE,de;q=0.9,en;q=0.8", ["de-DE", "de", "en"], "de-DE"),
            ("de-DE,de;q=0.9", ["de-DE", "de"], "de-DE"),
        ],
        "timezones": ["Europe/Berlin"],
        "locale": "de-DE",
        "currency": "EUR",
        "addresses": [
            {"line1": "Friedrichstrasse 12", "city": "Berlin", "state": "Berlin", "state_code": "BE", "zip": "10117"},
            {"line1": "Leopoldstrasse 30", "city": "Muenchen", "state": "Bayern", "state_code": "BY", "zip": "80802"},
        ],
    },
    "FR": {
        "languages": [
            ("fr-FR,fr;q=0.9,en;q=0.8", ["fr-FR", "fr", "en"], "fr-FR"),
            ("fr-FR,fr;q=0.9", ["fr-FR", "fr"], "fr-FR"),
        ],
        "timezones": ["Europe/Paris"],
        "locale": "fr-FR",
        "currency": "EUR",
        "addresses": [
            {"line1": "24 rue du Faubourg Saint-Antoine", "city": "Paris", "state": "Ile-de-France", "state_code": "IDF", "zip": "75012"},
            {"line1": "8 rue Sainte-Catherine", "city": "Bordeaux", "state": "Nouvelle-Aquitaine", "state_code": "NAQ", "zip": "33000"},
        ],
    },
    "NL": {
        "languages": [
            ("nl-NL,nl;q=0.9,en;q=0.8", ["nl-NL", "nl", "en"], "nl-NL"),
            ("en-GB,en;q=0.9,nl;q=0.8", ["en-GB", "en", "nl"], "en-GB"),
        ],
        "timezones": ["Europe/Amsterdam"],
        "locale": "nl-NL",
        "currency": "EUR",
        "addresses": [
            {"line1": "Damrak 45", "city": "Amsterdam", "state": "Noord-Holland", "state_code": "NH", "zip": "1012 LK"},
            {"line1": "Coolsingel 20", "city": "Rotterdam", "state": "Zuid-Holland", "state_code": "ZH", "zip": "3011 AD"},
        ],
    },
    "IE": {
        "languages": [
            ("en-IE,en;q=0.9", ["en-IE", "en"], "en-IE"),
            ("en-GB,en;q=0.9", ["en-GB", "en"], "en-GB"),
        ],
        "timezones": ["Europe/Dublin"],
        "locale": "en-IE",
        "currency": "EUR",
        "addresses": [
            {"line1": "12 Grafton Street", "city": "Dublin", "state": "Dublin", "state_code": "D", "zip": "D02 XY45"},
            {"line1": "5 Patrick Street", "city": "Cork", "state": "Cork", "state_code": "CO", "zip": "T12 XY45"},
        ],
    },
    "AU": {
        "languages": [
            ("en-AU,en;q=0.9", ["en-AU", "en"], "en-AU"),
            ("en-GB,en;q=0.9,en-AU;q=0.8", ["en-GB", "en", "en-AU"], "en-GB"),
        ],
        "timezones": ["Australia/Sydney", "Australia/Melbourne", "Australia/Brisbane"],
        "locale": "en-AU",
        "currency": "AUD",
        "addresses": [
            {"line1": "18 Flinders Lane", "city": "Melbourne", "state": "Victoria", "state_code": "VIC", "zip": "3000"},
            {"line1": "55 George Street", "city": "Sydney", "state": "New South Wales", "state_code": "NSW", "zip": "2000"},
        ],
    },
    "JP": {
        "languages": [
            ("ja-JP,ja;q=0.9,en;q=0.8", ["ja-JP", "ja", "en"], "ja-JP"),
            ("ja-JP,ja;q=0.9", ["ja-JP", "ja"], "ja-JP"),
        ],
        "timezones": ["Asia/Tokyo"],
        "locale": "ja-JP",
        "currency": "JPY",
        "addresses": [
            {"line1": "1-2-3 Shibuya", "city": "Tokyo", "state": "Tokyo", "state_code": "13", "zip": "150-0002"},
            {"line1": "4-5-6 Umeda", "city": "Osaka", "state": "Osaka", "state_code": "27", "zip": "530-0001"},
        ],
    },
    "SG": {
        "languages": [
            ("en-SG,en;q=0.9", ["en-SG", "en"], "en-SG"),
            ("en-GB,en;q=0.9", ["en-GB", "en"], "en-GB"),
        ],
        "timezones": ["Asia/Singapore"],
        "locale": "en-SG",
        "currency": "SGD",
        "addresses": [
            {"line1": "10 Anson Road", "city": "Singapore", "state": "Singapore", "state_code": "SG", "zip": "079903"},
            {"line1": "1 Raffles Place", "city": "Singapore", "state": "Singapore", "state_code": "SG", "zip": "048616"},
        ],
    },
}

# 屏幕组合：必须是真实存在的分辨率对
SCREENS = [
    (1920, 1080, 1.0), (2560, 1440, 1.0), (1680, 1050, 2.0),
    (1728, 1117, 2.0), (1512, 982, 2.0), (1440, 900, 2.0),
    (2560, 1600, 2.0), (3840, 2160, 1.5),
]

# 本机真实 GPU（启动时探测一次）。
#
# 为什么要探测：我们跑在真 Google Chrome 里，WebGL 本来就是真的。
# 如果画像硬写 "Apple M3 / NVIDIA",而容器/宿主机实际是别的 GPU，
# 等于用 JS 假值覆盖一个本来正确的真值 —— 反而制造矛盾。
# 所以：探测到真 GPU 就照搬（连同它的确是真值这点一起），
#       探测不到（GPU 禁用 / SwiftShader）才用画像值兜底。
_REAL_GPU: dict | None = None


def probe_real_gpu() -> dict | None:
    """用一次极小的 headless 探测拿到本机真实 WebGL vendor/renderer。

    只探一次，进程内缓存。失败返回 None（调用方用画像值兜底）。
    """
    global _REAL_GPU
    if _REAL_GPU is not None:
        return _REAL_GPU or None
    try:
        from playwright.sync_api import sync_playwright
        from core.browser import resolve_browser_binary
        # 和 SignupBrowser 用同一个二进制，否则探测结果和实际运行环境不一致
        exe, kw = resolve_browser_binary()
        if exe:
            kw["executable_path"] = exe
        with sync_playwright() as pw:
            br = pw.chromium.launch(headless=True, **kw)
            pg = br.new_page()
            info = pg.evaluate("""() => {
              try {
                const c = document.createElement('canvas').getContext('webgl');
                const e = c.getExtension('WEBGL_debug_renderer_info');
                return {v: c.getParameter(37445), r: c.getParameter(37446)};
              } catch (err) { return null; }
            }""")
            br.close()
    except Exception:
        info = None

    if not info or not info.get("r") or "SwiftShader" in (info.get("r") or ""):
        _REAL_GPU = {}
        return None
    _REAL_GPU = info
    return info


# 合理的网络质量（真实住宅，不是代理链路）
NETWORK_QUALITY = [
    (10.0, 50), (8.5, 75), (6.25, 100), (9.75, 60),
    (7.5, 90), (12.0, 45), (5.5, 120),
]


class UnknownCountry(ValueError):
    """出口国不在 GEO_PROFILES 里。

    以前这种情况会静默套用 CA 画像，结果是"代理在美国、语言/时区/账单在加拿大"，
    比注册失败更糟。现在直接抛错，让调用方补画像或换节点。
    """


def resolve_country(exit_country: str | None, fallback: str | None = None) -> str:
    """把代理实测出口国解析成画像国家。

    优先用实测值（cloudflare trace 的 loc），其次用人工配置的 region。
    两者都不在画像表里就抛 UnknownCountry。
    """
    for raw in (exit_country, fallback):
        if not raw:
            continue
        code = str(raw).strip().upper()
        if code in GEO_PROFILES:
            return code
    raise UnknownCountry(
        f"出口国 {exit_country!r} / 回退 {fallback!r} 都不在 GEO_PROFILES 里；"
        f"已支持: {', '.join(sorted(GEO_PROFILES))}。"
        f"请补画像或换到已支持的节点，不要静默套用其他国家。"
    )


@dataclass
class DeviceProfile:
    """一个账号绑定的完整设备画像"""

    # UA / Client Hints
    ua: str
    ch_ua: str
    ch_ua_full_version: str          # 带引号，直接进 HTTP 头
    full_version: str                # 裸值，进 JS
    ch_ua_full_version_list: str
    ch_ua_platform: str
    ch_ua_platform_version: str
    ch_ua_arch: str
    ch_ua_bitness: str
    ch_ua_mobile: str
    ch_ua_model: str
    ch_ua_wow64: str
    ch_ua_form_factors: str

    # 语言 / 时区 / 地区
    accept_language: str
    navigator_languages: list[str]
    navigator_language: str
    timezone: str
    locale: str
    country: str

    # 屏幕
    screen_w: int
    screen_h: int
    device_pixel_ratio: float

    # 网络
    downlink: float
    rtt: int

    # WebGL
    webgl_vendor: str
    webgl_renderer: str

    # navigator.platform（"MacIntel" / "Win32"）
    nav_platform: str = "MacIntel"

    # Not:A-Brand 的版本号（随 Chrome 大版本变化）
    brand_version: str = "8"
    brand_full: str = "8.0.0.0"

    # 硬件：必须和平台自洽，不能泄露宿主 CPU 数
    hardware_concurrency: int = 8
    device_memory: int = 8

    # 桌面端菜单栏/任务栏高度，用于 screen.availHeight
    menu_bar_px: int = 25

    # 绑定邮箱（用于派生稳定指纹）
    seed: str = ""

    @property
    def platform_name(self) -> str:
        """sec-ch-ua-platform 去引号后的值（"macOS" / "Windows"）"""
        return self.ch_ua_platform.strip('"')

    @property
    def accept_language_plain(self) -> str:
        """不带 q 值的语言列表。

        CDP 的 Network.setUserAgentOverride.acceptLanguage 会被 Chrome 直接
        按逗号切分写进 navigator.languages，带 q 值就会变成
        ["fr-CA", "fr;q=0.9", "en;q=0.8"] —— 真浏览器不可能出现这种值。
        所以 JS 侧用这个裸列表，HTTP 头再用带 q 值的 accept_language 覆盖。
        """
        return ",".join(self.navigator_languages)

    @property
    def avail_height(self) -> int:
        return max(1, self.screen_h - self.menu_bar_px)

    def headers(self) -> dict[str, str]:
        """XHR / fetch 类请求的头集合。

        ⚠️ 这里**不能**出现 downlink / rtt / dpr / viewport-width：
        它们是 JS 属性（navigator.connection.downlink、devicePixelRatio、
        innerWidth），浏览器从不把它们当 HTTP 头发出去。之前发了这四个，
        等于自报"我不是浏览器"。
        """
        return {
            "User-Agent": self.ua,
            "sec-ch-ua": self.ch_ua,
            "sec-ch-ua-full-version": self.ch_ua_full_version,
            "sec-ch-ua-full-version-list": self.ch_ua_full_version_list,
            "sec-ch-ua-platform": self.ch_ua_platform,
            "sec-ch-ua-platform-version": self.ch_ua_platform_version,
            "sec-ch-ua-arch": self.ch_ua_arch,
            "sec-ch-ua-bitness": self.ch_ua_bitness,
            "sec-ch-ua-mobile": self.ch_ua_mobile,
            "sec-ch-ua-model": self.ch_ua_model,
            "sec-ch-ua-wow64": self.ch_ua_wow64,
            "sec-ch-ua-form-factors": self.ch_ua_form_factors,
            "Accept-Language": self.accept_language,
            "Accept": "application/json",
            "sec-fetch-dest": "empty",
            "sec-fetch-mode": "cors",
            "sec-fetch-site": "same-site",
            "priority": "u=1, i",
        }

    def nav_headers(self) -> dict[str, str]:
        """顶层导航请求的头集合（Kratos 的 /self-service/*/browser 等）。

        真浏览器导航时是 document + navigate + sec-fetch-user，Accept 是 HTML。
        用 XHR 那套头去打导航端点，是"非浏览器客户端"的最短判定路径。
        """
        return {
            "User-Agent": self.ua,
            "sec-ch-ua": self.ch_ua,
            "sec-ch-ua-full-version": self.ch_ua_full_version,
            "sec-ch-ua-full-version-list": self.ch_ua_full_version_list,
            "sec-ch-ua-platform": self.ch_ua_platform,
            "sec-ch-ua-platform-version": self.ch_ua_platform_version,
            "sec-ch-ua-arch": self.ch_ua_arch,
            "sec-ch-ua-bitness": self.ch_ua_bitness,
            "sec-ch-ua-mobile": self.ch_ua_mobile,
            "sec-ch-ua-model": self.ch_ua_model,
            "sec-ch-ua-wow64": self.ch_ua_wow64,
            "sec-ch-ua-form-factors": self.ch_ua_form_factors,
            "Accept-Language": self.accept_language,
            "Accept": ("text/html,application/xhtml+xml,application/xml;q=0.9,"
                       "image/avif,image/webp,image/apng,*/*;q=0.8,"
                       "application/signed-exchange;v=b3;q=0.7"),
            "Upgrade-Insecure-Requests": "1",
            "sec-fetch-site": "none",
            "sec-fetch-mode": "navigate",
            "sec-fetch-user": "?1",
            "sec-fetch-dest": "document",
        }

    def console_js(self) -> str:
        """注入到浏览器的指纹覆写脚本（Playwright add_init_script 用）

        实现要点：
          - 用 %-格式化，避免 f-string 的 {{ }} 转义把 JSON 引号拼坏
          - 所有 defineProperty 都带 enumerable: true（真 Chrome 的属性是可枚举的）
          - getter 用 Function.prototype.toString 掩码，返回 native code 假象
          - 不碰 Intl：Playwright 的 timezone_id / locale 已经原生生效，
            之前用 Proxy 包 Intl.DateTimeFormat 反而成了最容易被检测的篡改点
        """
        return _JS_TEMPLATE % {
            "screen_w": self.screen_w,
            "screen_h": self.screen_h,
            "avail_h": self.avail_height,
            "dpr": repr(float(self.device_pixel_ratio)),
            "hc": int(self.hardware_concurrency),
            "dm": int(self.device_memory),
            "downlink": repr(float(self.downlink)),
            "rtt": int(self.rtt),
            "gl_vendor_js": json.dumps(self.webgl_vendor),
            "gl_renderer_js": json.dumps(self.webgl_renderer),
        }


# 用 %-格式化，JS 里的 { } 不需要转义；JS 里没有 % 字面量，安全。
_JS_TEMPLATE = r"""
(() => {
  "use strict";

  // ---- 让被替换的函数看起来还是 native ----
  const NATIVE_TO_STRING = Function.prototype.toString;
  const FAKE = new WeakMap();
  const mark = (fn, text) => { FAKE.set(fn, text); return fn; };
  const patchedToString = function toString() {
    const fake = FAKE.get(this);
    if (fake !== undefined) return fake;
    return NATIVE_TO_STRING.call(this);
  };
  mark(patchedToString, "function toString() { [native code] }");
  Function.prototype.toString = patchedToString;

  const defineGetter = (target, prop, value) => {
    const getter = function () { return value; };
    mark(getter, "function get " + prop + "() { [native code] }");
    Object.defineProperty(target, prop, {
      get: getter,
      set: undefined,
      enumerable: true,
      configurable: true,
    });
  };

  // UA / Client Hints / 语言 / platform 由 CDP Network.setUserAgentOverride
  // 在网络层原生设置（navigator.userAgentData 仍是真正的 NavigatorUAData 实例），
  // 这里不再用 JS 补丁 —— 补丁版 instanceof 会失败、getter 也不是 native。

  // ---- 屏幕 ----
  defineGetter(Screen.prototype, "width", %(screen_w)d);
  defineGetter(Screen.prototype, "height", %(screen_h)d);
  defineGetter(Screen.prototype, "availWidth", %(screen_w)d);
  defineGetter(Screen.prototype, "availHeight", %(avail_h)d);
  defineGetter(window, "devicePixelRatio", %(dpr)s);

  // ---- 硬件：不能泄露宿主 CPU 数 ----
  defineGetter(Navigator.prototype, "hardwareConcurrency", %(hc)d);
  defineGetter(Navigator.prototype, "deviceMemory", %(dm)d);

  // ---- 网络：改原型上的 getter，保留 NetworkInformation 实例 ----
  if (window.NetworkInformation) {
    defineGetter(NetworkInformation.prototype, "downlink", %(downlink)s);
    defineGetter(NetworkInformation.prototype, "rtt", %(rtt)d);
    defineGetter(NetworkInformation.prototype, "effectiveType", "4g");
    defineGetter(NetworkInformation.prototype, "saveData", false);
  }

  // ---- WebGL ----
  const patchGL = (proto) => {
    if (!proto) return;
    const original = proto.getParameter;
    const patched = function getParameter(p) {
      if (p === 37445) return %(gl_vendor_js)s;
      if (p === 37446) return %(gl_renderer_js)s;
      return original.call(this, p);
    };
    mark(patched, "function getParameter() { [native code] }");
    proto.getParameter = patched;
  };
  patchGL(window.WebGLRenderingContext && WebGLRenderingContext.prototype);
  patchGL(window.WebGL2RenderingContext && WebGL2RenderingContext.prototype);
})();
"""


def build_profile(email: str, country: str = "CA", platform_key: str | None = None,
                  chrome_key: str = "156") -> DeviceProfile:
    """
    从邮箱派生一个稳定的设备画像。
    同一个邮箱永远得到同一套画像（可复现），不同邮箱差异明显。

    country 必须是 GEO_PROFILES 里的键；未知国家抛 UnknownCountry，
    不再静默回落 CA（那会造成"IP 在美国、画像在加拿大"）。
    """
    country = str(country or "").strip().upper()
    if country not in GEO_PROFILES:
        raise UnknownCountry(
            f"不支持的国家 {country!r}；已支持: {', '.join(sorted(GEO_PROFILES))}。"
            f"请补画像或换节点，不要静默套用其他国家。"
        )

    h = hashlib.sha256(email.encode()).hexdigest()
    rnd = random.Random(h)

    geo = GEO_PROFILES[country]
    plat_key = platform_key or rnd.choice(list(PLATFORMS.keys()))
    plat = PLATFORMS[plat_key]
    build = CHROME_BUILDS[chrome_key]

    lang_hdr, langs, lang_prim = rnd.choice(geo["languages"])
    tz = rnd.choice(geo["timezones"])
    screen = rnd.choice(SCREENS)
    net = rnd.choice(NETWORK_QUALITY)
    hc = rnd.choice(plat["hardware_concurrency"])
    dm = rnd.choice(plat["device_memory"])

    ua = (f"Mozilla/5.0 {plat['ua_platform']} AppleWebKit/537.36 "
          f"(KHTML, like Gecko) Chrome/{build['major']}.0.0.0 Safari/537.36")

    major = build["major"]
    brands_ua = (f'"Not:A-Brand";v="{build["brand_version"]}", '
                 f'"Chromium";v="{major}", "Google Chrome";v="{major}"')
    brands_full = (f'"Not:A-Brand";v="{build["brand_full"]}", '
                   f'"Chromium";v="{build["full"]}", "Google Chrome";v="{build["full"]}"')

    prof = DeviceProfile(
        ua=ua,
        ch_ua=brands_ua,
        ch_ua_full_version=f'"{build["full"]}"',
        full_version=build["full"],
        ch_ua_full_version_list=brands_full,
        ch_ua_platform=plat["ch_platform"],
        ch_ua_platform_version=f'"{plat["platform_version"]}"',
        ch_ua_arch=plat["arch"],
        ch_ua_bitness=plat["bitness"],
        ch_ua_mobile="?0",
        ch_ua_model='""',
        ch_ua_wow64=plat["wow64"],
        ch_ua_form_factors='"Desktop"',
        accept_language=lang_hdr,
        navigator_languages=langs,
        navigator_language=lang_prim,
        timezone=tz,
        locale=geo["locale"],
        country=country,
        screen_w=screen[0],
        screen_h=screen[1],
        device_pixel_ratio=screen[2],
        downlink=net[0],
        rtt=net[1],
        webgl_vendor=(probe_real_gpu() or {}).get("v") or plat["webgl_vendor"],
        webgl_renderer=(probe_real_gpu() or {}).get("r") or plat["webgl_renderer"],
        nav_platform=plat["nav_platform"],
        brand_version=build["brand_version"],
        brand_full=build["brand_full"],
        hardware_concurrency=hc,
        device_memory=dm,
        menu_bar_px=plat["menu_bar_px"],
        seed=h,
    )
    return prof


def billing_address(email: str, country: str = "CA") -> dict:
    """账单地址：必须与 IP 同国（抓包里 US 地址配 CA IP 是破绽）"""
    country = str(country or "").strip().upper()
    if country not in GEO_PROFILES:
        raise UnknownCountry(
            f"账单地址不支持 {country!r}；已支持: {', '.join(sorted(GEO_PROFILES))}"
        )
    h = hashlib.sha256((email + ":addr").encode()).hexdigest()
    rnd = random.Random(h)
    geo = GEO_PROFILES[country]
    a = dict(rnd.choice(geo["addresses"]))
    a["country"] = country
    a["currency"] = geo["currency"]
    return a
