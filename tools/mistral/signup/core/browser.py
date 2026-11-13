"""
浏览器注册流程（Playwright + 真实 Chrome）

为什么必须用浏览器：
  - TLS/JA3 指纹无法用 HTTP 客户端伪造（实测 curl JA3=375c6162... vs Chrome 差异巨大）
  - Client Hints 12 个字段由浏览器原生生成，天然一致
  - Cloudflare Turnstile 需要真实浏览器环境
  - Axept 行为检测需要真实鼠标/键盘事件

代理：每账号一条 zooproxy 住宅链路（经本地 sing-box）
指纹：--disable-blink-features=AutomationControlled + CDP 注入
"""
from __future__ import annotations

import os
import random
import shutil
import time
from dataclasses import dataclass
from typing import Callable, Optional

from playwright.sync_api import Browser, BrowserContext, Page, sync_playwright

from core.fingerprint import DeviceProfile

# ---------------------------------------------------------------- 二进制选择
#
# Playwright 自带的 Chromium 和真 Google Chrome 在页面里长得不一样：
#   - Playwright Chromium: navigator.plugins.length == 0
#                          window.chrome == undefined
#                          无 Widevine / 无 PDF 查看器
#   - 真 Google Chrome:    navigator.plugins.length == 5 (PDF 三件套 + 2)
#                          window.chrome == {loadTimes, csi, app}
#
# 任何 fingerprintjs 级别的库都直接读这两项，所以必须优先用真 Chrome。
#
# 探测顺序：
#   1. MISTRAL_BROWSER_CHANNEL 环境变量（chrome / chromium）
#   2. 系统装的真 Google Chrome（macOS / Linux 常见路径）
#   3. Playwright 自带 Chromium（最后兜底，会打 warning）
def _candidate_chrome_paths() -> list[str]:
    if os.name == "nt":  # Windows
        roots = [os.environ.get("PROGRAMFILES", r"C:\Program Files"),
                 os.environ.get("PROGRAMFILES(X86)", r"C:\Program Files (x86)"),
                 os.environ.get("LOCALAPPDATA", "")]
        return [os.path.join(r, "Google", "Chrome", "Application", "chrome.exe")
                for r in roots if r]
    if os.uname().sysname == "Darwin":
        return ["/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"]
    return [
        "/usr/bin/google-chrome",
        "/usr/bin/google-chrome-stable",
        "/opt/google/chrome/chrome",
    ]


def resolve_browser_binary() -> tuple[str, dict]:
    """返回 (executable_path, launch_kwargs)。

    launch_kwargs 会带 channel="chrome"，让 Playwright 用 Chrome 专属的
    启动参数模板（而不是 Chromium 模板）。
    """
    forced = (os.environ.get("MISTRAL_BROWSER_CHANNEL") or "").strip().lower()
    if forced == "chromium":
        return "", {}
    if forced == "chrome":
        return "", {"channel": "chrome"}

    for path in _candidate_chrome_paths():
        if path and os.path.isfile(path):
            return path, {"channel": "chrome"}
    return "", {}


# 真 Chrome 的 navigator.plugins 内容（PDF 三件套 + 2 个内置）
CHROME_PDF_PLUGINS = [
    {"name": "PDF Viewer", "filename": "internal-pdf-viewer",
     "description": "Portable Document Format"},
    {"name": "Chrome PDF Viewer", "filename": "internal-pdf-viewer",
     "description": "Portable Document Format"},
    {"name": "Chromium PDF Viewer", "filename": "internal-pdf-viewer",
     "description": "Portable Document Format"},
    {"name": "Microsoft Edge PDF Viewer", "filename": "internal-pdf-viewer",
     "description": "Portable Document Format"},
    {"name": "WebKit built-in PDF", "filename": "internal-pdf-viewer",
     "description": "Portable Document Format"},
]


@dataclass
class BrowserOptions:
    # 默认非 headless：新 headless 的 WebGL 是 SwiftShader，
    # 与画像声称的 Apple/NVIDIA GPU 直接矛盾。容器里配 xvfb 后跑 headed。
    headless: bool = False
    user_data_base: str = ""
    slow_mo: int = 0
    # 覆盖浏览器二进制（空 = 自动探测真 Chrome）
    executable_path: str = ""


class SignupBrowser:
    """
    单账号浏览器实例。
    每个账号: 独立 context + 独立代理 + 独立指纹
    """

    def __init__(self, profile: DeviceProfile, proxy_server: str,
                 opts: BrowserOptions, regional_locale: str = None):
        self.profile = profile
        self.proxy_server = proxy_server      # 例如 socks5://127.0.0.1:64817
        self.opts = opts
        self.user_agent = profile.ua
        # 浏览器窗口比屏幕矮：真 Chrome 有标签栏 + 地址栏 + 书签栏。
        # 之前 viewport 直接等于整屏，导致 innerHeight == screen.height，
        # 物理上不可能（窗口没有 chrome）。
        self.chrome_px = 120
        self.viewport_w = profile.screen_w
        self.viewport_h = max(400, profile.screen_h - self.chrome_px)
        self.launch_args = [
            "--disable-blink-features=AutomationControlled",
            # 注意：不要加 --disable-features=IsolateOrigins,site-per-process
            # 它会改掉 originAgentCluster / crossOriginIsolated 的行为，
            # 本身就是自动化标志，而且对注册流程没有任何帮助。
            "--no-first-run",
            "--no-default-browser-check",
            "--disable-infobars",
            f"--lang={profile.locale}",
            "--window-size={},{}".format(self.viewport_w, self.viewport_h + self.chrome_px),
        ]

    def __enter__(self):
        self._pw = sync_playwright().start()
        self._launch()
        self._build_context()
        return self

    def _launch(self):
        """启动浏览器：优先真 Chrome，headless 不行就自动退到 headed。"""
        exe, kwargs = ("", {})
        if self.opts.executable_path:
            exe, kwargs = self.opts.executable_path, {}
        else:
            exe, kwargs = resolve_browser_binary()
        if exe:
            kwargs["executable_path"] = exe

        try:
            self.browser = self._pw.chromium.launch(
                headless=self.opts.headless,
                args=self.launch_args,
                slow_mo=self.opts.slow_mo,
                **kwargs,
            )
        except Exception as e:
            # headless 模式下 hub 没装 / 被禁用时退到 headed（配合 xvfb）。
            # 反过来的情况下（无 DISPLAY）就老老实实报错，不静默降级。
            if self.opts.headless:
                print(f"[browser] headless launch failed ({e}); retrying headed")
                self.browser = self._pw.chromium.launch(
                    headless=False,
                    args=self.launch_args,
                    slow_mo=self.opts.slow_mo,
                    **kwargs,
                )
            else:
                raise

        # 记下实际用的是什么，便于诊断"为什么 plugins 还是 0"
        try:
            ver = self.browser.version
        except Exception:
            ver = "?"
        self.binary = exe or ("playwright-chromium" if not kwargs.get("channel")
                              else "channel:chrome")
        # 只有真 Chrome 才会带上 "Chrome/" 而不是 "HeadlessChrome/"
        self.is_real_chrome = "HeadlessChrome" not in (ver or "")
        print(f"[browser] binary={self.binary} version={ver} "
              f"headless={self.opts.headless} real_chrome={self.is_real_chrome}")
    def _build_context(self):
        # Playwright 要求用户名/密码分开传，不能塞在 URL 里，
        # 否则会返回 "Proxy Authentication needed"
        proxy_cfg = self._parse_proxy(self.proxy_server)
        self.context = self.browser.new_context(
            proxy=proxy_cfg,
            user_agent=self.user_agent,
            locale=self.profile.locale,
            timezone_id=self.profile.timezone,
            viewport={"width": self.viewport_w, "height": self.viewport_h},
            screen={"width": self.profile.screen_w, "height": self.profile.screen_h},
            device_scale_factor=self.profile.device_pixel_ratio,
            is_mobile=False,
            has_touch=False,
            java_script_enabled=True,
            color_scheme="light",
            # Playwright Chromium 的 navigator.plugins 是空数组（真 Chrome 有 5 个 PDF 插件），
            # 而真 Chrome 不接受 new_context(plugins=...)（它本来就自带），所以只在
            # 非真 Chrome 时补上。探测方法见 __init__ 里的 is_real_chrome。
            **({"plugins": CHROME_PDF_PLUGINS} if not self.is_real_chrome else {}),
            extra_http_headers={"Accept-Language": self.profile.accept_language},
        )
        # 注入指纹覆写（在页面脚本前执行）
        self.context.add_init_script(self.profile.console_js())
        # 抹除 webdriver 痕迹
        #
        # 注意：不要写 window.chrome = window.chrome || { runtime: {} }。
        # 真 Chrome 的 window.chrome 有 loadTimes / csi / app，
        # 用 { runtime: {} } 顶掉等于把它们全删了，反而新增一个 headless 特征。
        self.context.add_init_script("""
            Object.defineProperty(navigator, 'webdriver', { get: () => undefined });
            const origQuery = window.navigator.permissions.query;
            window.navigator.permissions.query = (p) => (
                p.name === 'notifications'
                    ? Promise.resolve({ state: Notification.permission })
                    : origQuery(p)
            );
        """)
        self.page = self.context.new_page()

        # ---------------------------------------------------------------
        # 关键：用 CDP 强制覆盖 HTTP 层 Client Hints
        #
        # Playwright 的 Chromium 默认发出：
        #   sec-ch-ua: "Not:A-Brand";v="99", "HeadlessChrome";v="145", "Chromium";v="145"
        #   ^^^ HeadlessChrome 品牌 + v="99" 非发行标记，cloudflare 一眼识破
        #
        # add_init_script 只改 JS 侧 navigator.userAgentData，
        # 改不了真实 HTTP 头，必须走 Network.setExtraHTTPHeaders。
        # ---------------------------------------------------------------
        self._apply_header_override()

    @staticmethod
    def _parse_proxy(url: str | None) -> dict | None:
        """把 http://user:pass@host:port 拆成 Playwright 需要的结构。
        None / 空串表示直连（OVH 海外机器可直接访问目标站）。"""
        if not url:
            return None
        import urllib.parse as up
        u = up.urlparse(url if "://" in url else "http://" + url)
        cfg = {"server": f"{u.scheme or 'http'}://{u.hostname}:{u.port}"}
        if u.username:
            cfg["username"] = up.unquote(u.username)
            cfg["password"] = up.unquote(u.password or "")
        return cfg

    def _apply_header_override(self):
        """在网络层把 UA / Client Hints / 语言 / platform 一次性对齐。

        为什么用 Network.setUserAgentOverride 而不是 JS 补丁：
          - navigator.userAgentData 会保持真正的 NavigatorUAData 实例
            （JS 补丁版 instanceof 失败、getter 不是 native、属性不可枚举）
          - sec-ch-ua* 请求头与 JS 值由同一个来源生成，天然一致
          - 之前用 add_init_script 改 JS、用 setExtraHTTPHeaders 改头，
            两边一旦有一处拼错（2026-11-09 就拼错了 4 处），
            结果就是 JS 报 HeadlessChrome/145、头报 Chrome/156
        """
        try:
            cdp = self.context.new_cdp_session(self.page)
            cdp.send("Network.enable")
            p = self.profile
            cdp.send("Network.setUserAgentOverride", {
                "userAgent": p.ua,
                # 裸语言列表：Chrome 会按逗号切分直接写进 navigator.languages，
                # 带 q 值会变成 ["fr-CA", "fr;q=0.9"] 这种不可能的值
                "acceptLanguage": p.accept_language_plain,
                "platform": p.nav_platform,
                "userAgentMetadata": {
                    "brands": [
                        {"brand": "Not:A-Brand", "version": p.brand_version},
                        {"brand": "Chromium", "version": p.full_version.split(".")[0]},
                        {"brand": "Google Chrome", "version": p.full_version.split(".")[0]},
                    ],
                    "fullVersionList": [
                        {"brand": "Not:A-Brand", "version": p.brand_full},
                        {"brand": "Chromium", "version": p.full_version},
                        {"brand": "Google Chrome", "version": p.full_version},
                    ],
                    "fullVersion": p.full_version,
                    "platform": p.platform_name,
                    "platformVersion": p.ch_ua_platform_version.strip('"'),
                    "architecture": p.ch_ua_arch.strip('"'),
                    "model": "",
                    "mobile": False,
                    "bitness": p.ch_ua_bitness.strip('"'),
                    "wow64": False,
                    "formFactors": ["Desktop"],
                },
            })
            # 头部再用带 q 值的 Accept-Language 覆盖（实测这一层优先级更高），
            # 这样 navigator.languages 是裸列表、HTTP 头是带 q 值的标准格式。
            cdp.send("Network.setExtraHTTPHeaders", {
                "headers": {"Accept-Language": p.accept_language},
            })
            self._cdp = cdp
        except Exception as e:
            # 不阻断流程，但要留痕
            print(f"[browser] ua override failed: {e}")

    def __exit__(self, *a):
        try:
            self.context.close()
        except Exception:
            pass
        try:
            self.browser.close()
        except Exception:
            pass
        try:
            self._pw.stop()
        except Exception:
            pass

    # ---------- 拟人交互 ----------
    def human_type(self, page: Page, selector: str, text: str):
        """逐字符输入，带随机延迟"""
        page.click(selector)
        time.sleep(random.uniform(0.15, 0.4))
        for ch in text:
            page.type(selector, ch, delay=random.randint(40, 160))
        time.sleep(random.uniform(0.2, 0.6))

    def human_move(self, page: Page, x: int, y: int, steps: int = 12):
        """贝塞尔近似鼠标轨迹"""
        start_x, start_y = 0, 0
        cx1 = start_x + (x - start_x) * random.uniform(0.2, 0.4)
        cy1 = start_y + (y - start_y) * random.uniform(0.1, 0.5)
        cx2 = start_x + (x - start_x) * random.uniform(0.6, 0.8)
        cy2 = start_y + (y - start_y) * random.uniform(0.5, 0.9)
        for i in range(steps + 1):
            t = i / steps
            bx = (1-t)**3*start_x + 3*(1-t)**2*t*cx1 + 3*(1-t)*t**2*cx2 + t**3*x
            by = (1-t)**3*start_y + 3*(1-t)**2*t*cy1 + 3*(1-t)*t**2*cy2 + t**3*y
            page.mouse.move(bx, by)
            time.sleep(random.uniform(0.01, 0.04))

    def human_pause(self, lo: float = 0.6, hi: float = 2.5):
        time.sleep(random.uniform(lo, hi))
