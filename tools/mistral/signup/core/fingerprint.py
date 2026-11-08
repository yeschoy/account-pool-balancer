"""
设备画像：每个账号绑定一套**内部完全自洽**的指纹。

设计原则（来自抓包分析）：
  1. UA + 12 个 Client Hints 必须整体匹配，不能只改 UA
  2. 语言 / 时区 / 地址 必须与代理出口国一致
  3. Canvas / WebGL / Audio 指纹在账号内固定，账号间不同

矛盾点：既要"随机 UA"避免批量特征，又要"固定指纹"显得像同一真人。
解法：账号级固定画像，账号间随机。
"""
from __future__ import annotations

import hashlib
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
PLATFORMS = {
    "macos-arm": {
        "ua_platform": "(Macintosh; Intel Mac OS X 10_15_7)",
        "ch_platform": '"macOS"',
        "platform_version": "15.7.7",
        "arch": '"arm"',
        "bitness": '"64"',
        "wow64": "?0",
        "webgl_vendor": "Google Inc. (Apple)",
        "webgl_renderer": "ANGLE (Apple, ANGLE Metal Renderer: Apple M3, Unspecified Version)",
    },
    "macos-intel": {
        # Intel Mac 最高只到 macOS 15.x 的特定版本，且 M 系列才会报 arm
        # 这里用一组真实存在的 Intel Mac 组合，避免与 arm 画像混淆
        "ua_platform": "(Macintosh; Intel Mac OS X 10_15_7)",
        "ch_platform": '"macOS"',
        "platform_version": "15.3.1",
        "arch": '"x86"',
        "bitness": '"64"',
        "wow64": "?0",
        "webgl_vendor": "Google Inc. (Intel Inc.)",
        "webgl_renderer": "ANGLE (Intel, Intel(R) Iris(TM) Plus Graphics 640, OpenGL 4.1)",
    },
    "windows": {
        "ua_platform": "(Windows NT 10.0; Win64; x64)",
        "ch_platform": '"Windows"',
        "platform_version": "15.0.0",
        "arch": '"x86"',
        "bitness": '"64"',
        "wow64": "?0",
        "webgl_vendor": "Google Inc. (NVIDIA)",
        "webgl_renderer": "ANGLE (NVIDIA, NVIDIA GeForce RTX 3060 (0x00002504) Direct3D11 vs_5_0 ps_5_0, D3D11)",
    },
}

# 出口国 -> 语言/时区/地址 画像
# 关键: 语言和时区必须与代理出口国一致（抓包里 CA 出口 + zh-CN 是致命破绽）
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
}

# 屏幕组合：必须是真实存在的分辨率对
SCREENS = [
    (1920, 1080, 1.0), (2560, 1440, 1.0), (1680, 1050, 2.0),
    (1728, 1117, 2.0), (1512, 982, 2.0), (1440, 900, 2.0),
    (2560, 1600, 2.0), (3840, 2160, 1.5),
]

# 合理的网络质量（真实住宅，不是代理链路）
NETWORK_QUALITY = [
    (10.0, 50), (8.5, 75), (6.25, 100), (9.75, 60),
    (7.5, 90), (12.0, 45), (5.5, 120),
]


@dataclass
class DeviceProfile:
    """一个账号绑定的完整设备画像"""

    # UA / Client Hints
    ua: str
    ch_ua: str
    ch_ua_full_version: str
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

    # 绑定邮箱（用于派生稳定指纹）
    seed: str = ""

    def headers(self) -> dict[str, str]:
        """构造一个真实 Chrome 会发出的完整 header 集合"""
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
            "Accept-Encoding": "gzip, deflate, br, zstd",
            "Accept": "application/json",
            "sec-fetch-dest": "empty",
            "sec-fetch-mode": "cors",
            "sec-fetch-site": "same-site",
            "priority": "u=1, i",
            "downlink": str(self.downlink),
            "rtt": str(self.rtt),
            "dpr": str(self.device_pixel_ratio),
            "viewport-width": f"{int(self.screen_w * 0.85)}",
        }

    def console_js(self) -> str:
        """注入到浏览器的指纹覆写脚本（Playwright add_init_script 用）"""
        return f"""
(() => {{
  // ---- userAgentData (Client Hints JS 端) ----
  const brands = [
    {{ brand: "Not:A-Brand", version: "8" }},
    {{ brand: "Chromium", version: "{self.ch_ua_full_version.split('.')[0]}" }},
    {{ brand: "Google Chrome", version: "{self.ch_ua_full_version.split('.')[0]}" }},
  ];
  const uaData = {{
    brands,
    mobile: false,
    platform: {self.ch_ua_platform},
    getHighEntropyValues: async (hints) => ({{
      architecture: {self.ch_ua_arch},
      bitness: {self.ch_ua_bitness},
      brands,
      fullVersionList: brands.map(b => ({{ ...b, version: "{self.ch_ua_full_version}" }})),
      mobile: false,
      model: "",
      platform: {self.ch_ua_platform},
      platformVersion: {self.ch_ua_platform_version},
      uaFullVersion: "{self.ch_ua_full_version}",
      wow64: false,
      formFactors: ["Desktop"],
    }}),
    toJSON() {{ return {{ brands, mobile: false, platform: {self.ch_ua_platform} }}; }},
  }};
  Object.defineProperty(Navigator.prototype, "userAgentData", {{ get: () => uaData, configurable: true }});

  // ---- 语言 ----
  Object.defineProperty(Navigator.prototype, "language", {{ get: () => {self.navigator_language!r}, configurable: true }});
  Object.defineProperty(Navigator.prototype, "languages", {{ get: () => {self.navigator_languages!r}, configurable: true }});

  // ---- 时区 ----
  const TZ = {self.timezone!r};
  const DTF = Intl.DateTimeFormat;
  Intl.DateTimeFormat = new Proxy(DTF, {{
    construct(target, args) {{
      if (!args[1] || !args[1].timeZone) args[1] = {{ ...(args[1] || {{}}), timeZone: TZ }};
      return new target(...args);
    }},
  }});
  DTF.prototype.resolvedOptions = new Proxy(DTF.prototype.resolvedOptions, {{
    apply(target, self, args) {{
      const r = Reflect.apply(target, self, args);
      r.timeZone = TZ;
      r.locale = {self.locale!r};
      return r;
    }},
  }});

  // ---- 平台 ----
  Object.defineProperty(Navigator.prototype, "platform", {{ get: () => {self.ch_ua_platform!r}.replace(/"/g, ""), configurable: true }});

  // ---- 屏幕 ----
  Object.defineProperty(Screen.prototype, "width", {{ get: () => {self.screen_w}, configurable: true }});
  Object.defineProperty(Screen.prototype, "height", {{ get: () => {self.screen_h}, configurable: true }});
  Object.defineProperty(Screen.prototype, "availWidth", {{ get: () => {self.screen_w}, configurable: true }});
  Object.defineProperty(Screen.prototype, "availHeight", {{ get: () => {self.screen_h - 25} }}, configurable: true }});
  Object.defineProperty(window, "devicePixelRatio", {{ get: () => {self.device_pixel_ratio}, configurable: true }});

  // ---- 网络 ----
  Object.defineProperty(Navigator.prototype, "connection", {{
    get: () => ({{ downlink: {self.downlink}, rtt: {self.rtt}, effectiveType: "4g", saveData: false }}),
    configurable: true,
  }});

  // ---- WebGL ----
  const patchGL = (proto) => {{
    const gp = proto.getParameter;
    proto.getParameter = function (p) {{
      if (p === 37445) return {self.webgl_vendor!r};
      if (p === 37446) return {self.webgl_renderer!r};
      return gp.call(this, p);
    }};
  }};
  if (window.WebGLRenderingContext) patchGL(WebGLRenderingContext.prototype);
  if (window.WebGL2RenderingContext) patchGL(WebGL2RenderingContext.prototype);
}})();
"""


def build_profile(email: str, country: str = "CA", platform_key: str | None = None,
                  chrome_key: str = "156") -> DeviceProfile:
    """
    从邮箱派生一个稳定的设备画像。
    同一个邮箱永远得到同一套画像（可复现），不同邮箱差异明显。
    """
    h = hashlib.sha256(email.encode()).hexdigest()
    rnd = random.Random(h)

    geo = GEO_PROFILES.get(country, GEO_PROFILES["CA"])
    plat_key = platform_key or rnd.choice(list(PLATFORMS.keys()))
    plat = PLATFORMS[plat_key]
    build = CHROME_BUILDS[chrome_key]

    lang_hdr, langs, lang_prim = rnd.choice(geo["languages"])
    tz = rnd.choice(geo["timezones"])
    screen = rnd.choice(SCREENS)
    net = rnd.choice(NETWORK_QUALITY)

    ua = (f"Mozilla/5.0 {plat['ua_platform']} AppleWebKit/537.36 "
          f"(KHTML, like Gecko) Chrome/{build['major']}.0.0.0 Safari/537.36")

    major = build["major"]
    brands_ua = (f'"Not:A-Brand";v="{build["brand_version"]}", '
                 f'"Chromium";v="{major}", "Google Chrome";v="{major}"')
    brands_full = (f'"Not:A-Brand";v="{build["brand_full"]}", '
                   f'"Chromium";v="{build["full"]}", "Google Chrome";v="{build["full"]}"')

    return DeviceProfile(
        ua=ua,
        ch_ua=brands_ua,
        ch_ua_full_version=f'"{build["full"]}"',
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
        webgl_vendor=plat["webgl_vendor"],
        webgl_renderer=plat["webgl_renderer"],
        seed=h,
    )


def billing_address(email: str, country: str = "CA") -> dict:
    """账单地址：必须与 IP 同国（抓包里 US 地址配 CA IP 是破绽）"""
    h = hashlib.sha256((email + ":addr").encode()).hexdigest()
    rnd = random.Random(h)
    geo = GEO_PROFILES.get(country, GEO_PROFILES["CA"])
    a = dict(rnd.choice(geo["addresses"]))
    a["country"] = country
    return a
