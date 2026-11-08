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

import random
import time
from dataclasses import dataclass
from typing import Callable, Optional

from playwright.sync_api import Browser, BrowserContext, Page, sync_playwright

from core.fingerprint import DeviceProfile


@dataclass
class BrowserOptions:
    headless: bool = False
    user_data_base: str = ""
    slow_mo: int = 0


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
        self.launch_args = [
            "--disable-blink-features=AutomationControlled",
            "--disable-features=IsolateOrigins,site-per-process",
            "--no-first-run",
            "--no-default-browser-check",
            "--disable-infobars",
            f"--lang={profile.locale}",
            "--window-size={},{}".format(profile.screen_w, profile.screen_h),
        ]

    def __enter__(self):
        self._pw = sync_playwright().start()
        self.browser = self._pw.chromium.launch(
            headless=self.opts.headless,
            args=self.launch_args,
            slow_mo=self.opts.slow_mo,
        )
        # Playwright 要求用户名/密码分开传，不能塞在 URL 里，
        # 否则会返回 "Proxy Authentication needed"
        proxy_cfg = self._parse_proxy(self.proxy_server)
        self.context = self.browser.new_context(
            proxy=proxy_cfg,
            user_agent=self.user_agent,
            locale=self.profile.locale,
            timezone_id=self.profile.timezone,
            viewport={"width": self.profile.screen_w, "height": self.profile.screen_h},
            screen={"width": self.profile.screen_w, "height": self.profile.screen_h},
            device_scale_factor=self.profile.device_pixel_ratio,
            is_mobile=False,
            has_touch=False,
            java_script_enabled=True,
            color_scheme="light",
            extra_http_headers={"Accept-Language": self.profile.accept_language},
        )
        # 注入指纹覆写（在页面脚本前执行）
        self.context.add_init_script(self.profile.console_js())
        # 抹除 webdriver 痕迹
        self.context.add_init_script("""
            Object.defineProperty(navigator, 'webdriver', { get: () => undefined });
            window.chrome = window.chrome || { runtime: {} };
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

        return self

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
        """把 HTTP 层的 sec-ch-ua-* / Accept-Language 换成画像里的值"""
        try:
            cdp = self.context.new_cdp_session(self.page)
            hdrs = self.profile.headers()
            force = {
                k: v for k, v in hdrs.items()
                if k.lower().startswith("sec-ch-ua") or k.lower() == "accept-language"
            }
            cdp.send("Network.enable")
            cdp.send("Network.setExtraHTTPHeaders", {"headers": force})
            self._cdp = cdp
        except Exception as e:
            # 不阻断流程，但要留痕
            print(f"[browser] header override failed: {e}")

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
