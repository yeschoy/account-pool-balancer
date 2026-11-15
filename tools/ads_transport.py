#!/usr/bin/env python3
"""
ads_transport.py —— 把 deploy 全流程跑在 AdsPower profile 里

设计目标
────────
一个号 = 一个 AdsPower profile = 一套固定的指纹/IP/状态。
注册、登录、建 Key、绑卡、订阅全部在**同一个浏览器进程**里完成，
请求由页面内的 fetch() 发出，因此：

  - TLS/HTTP2 指纹 = AdsPower 内核原生（不是 curl_cffi 拼的）
  - UA / sec-ch-ua / platform = profile 配置，全程不变
  - Cookie 由浏览器自己管，不用手工搬运 session_cookie
  - Origin / Referer 自然正确，不用伪造
  - 出口 IP = profile 绑定的代理，全程不变

这正好解决了旧方案里"注册走 curl_cffi、绑卡走 Playwright，
对服务端是两台不同设备"的断裂点。

使用
────
    from ads_transport import AdsProfile

    p = AdsProfile(api_key=KEY, profile_id="k1cet4ua")
    p.start()                       # 启动并等 CDP 就绪
    p.goto("https://deploy.com/signup?source=landing_login")
    r = p.post_json("https://ajax-portal.deploy.com/api/register", payload)
    print(r.status, r.body)
    p.stop()

依赖：websocket-client（pip install websocket-client）
"""

from __future__ import annotations

import json
import time
import urllib.parse
import urllib.request
from dataclasses import dataclass, field
from typing import Any

LOCAL_API = "http://local.adspower.net:50325"


# --------------------------------------------------------------------------
# 响应
# --------------------------------------------------------------------------

@dataclass
class AdsResponse:
    status: int
    body: str
    headers: dict[str, str] = field(default_factory=dict)
    error: str = ""

    @property
    def json(self) -> Any:
        try:
            return json.loads(self.body)
        except Exception:
            return None


# --------------------------------------------------------------------------
# 极简 CDP 客户端（websocket-client）
# --------------------------------------------------------------------------

class _CDP:
    """够用就行的 CDP 客户端：发命令、等回包、按 id 配对。"""

    def __init__(self, ws_url: str, timeout: float = 60.0) -> None:
        try:
            import websocket  # type: ignore  # noqa: PLC0415
        except ImportError as e:  # pragma: no cover
            raise RuntimeError(
                "需要 websocket-client：pip install websocket-client"
            ) from e
        # Chrome 的 CDP 会拒绝带 Origin 头的 WebSocket（403 Forbidden），
        # websocket-client 默认会带。这里显式不带 Origin 就能连上。
        self._ws = websocket.create_connection(
            ws_url,
            timeout=timeout,
            suppress_origin=True,
        )
        self._id = 0
        self._timeout = timeout
        self._events: list[dict] = []

    def send(self, method: str, params: dict | None = None,
             timeout: float | None = None) -> dict:
        self._id += 1
        mid = self._id
        self._ws.send(json.dumps({"id": mid, "method": method,
                                  "params": params or {}}))
        deadline = time.monotonic() + (timeout or self._timeout)
        while time.monotonic() < deadline:
            raw = self._ws.recv()
            if not raw:
                continue
            msg = json.loads(raw)
            if "method" in msg:
                self._events.append(msg)
                continue
            if msg.get("id") == mid:
                if "error" in msg:
                    raise RuntimeError(
                        f"CDP {method} 失败: {msg['error']}"
                    )
                return msg.get("result", {})
        raise TimeoutError(f"CDP {method} 超时")

    def send_session(self, session_id: str, method: str,
                     params: dict | None = None) -> dict:
        """在已 attach 的扁平会话（OOPIF）上执行命令。"""
        self._id += 1
        mid = self._id
        self._ws.send(json.dumps({"id": mid, "method": method,
                                  "params": params or {},
                                  "sessionId": session_id}))
        deadline = time.monotonic() + self._timeout
        while time.monotonic() < deadline:
            raw = self._ws.recv()
            if not raw:
                continue
            msg = json.loads(raw)
            if "method" in msg:
                self._events.append(msg)
                continue
            if msg.get("id") == mid:
                if "error" in msg:
                    raise RuntimeError(f"CDP[{method}] {msg['error']}")
                return msg.get("result", {})
        raise TimeoutError(f"CDP[{method}] 超时")

    def frames(self) -> list[dict]:
        """列出当前页面的所有 frame（含 iframe）。"""
        try:
            tree = self.send("Page.getFrameTree")
        except Exception:  # noqa: BLE001
            return []
        out: list[dict] = []

        def walk(node: dict) -> None:
            f = node.get("frame") or {}
            out.append(f)
            for c in node.get("childFrames") or []:
                walk(c)

        walk(tree.get("frameTree") or {})
        return out

    def eval_in_frame(self, frame_id: str, expr: str) -> Any:
        """在指定 iframe 的上下文里执行 JS。"""
        ctx = self.send("Page.createIsolatedWorld",
                        {"frameId": frame_id, "worldName": "rea"})
        cid = ctx.get("executionContextId")
        if not cid:
            raise RuntimeError("隔离上下文创建失败")
        res = self.send("Runtime.evaluate", {
            "expression": expr,
            "contextId": cid,
            "returnByValue": True,
            "awaitPromise": True,
        })
        return ((res.get("result") or {}).get("value"))

    def close(self) -> None:
        try:
            self._ws.close()
        except Exception:  # noqa: BLE001
            pass


# --------------------------------------------------------------------------
# profile 控制
# --------------------------------------------------------------------------

class AdsProfile:
    """
    一个 AdsPower profile 的生命周期 + 页面内请求能力。

    关键约束：**一个账号全程只用一个 profile**，不要中途换，
    否则指纹/IP 就变了，前面所有伪装白做。
    """

    def __init__(
        self,
        api_key: str,
        profile_id: str,
        *,
        headless: bool = False,
        cdp_mask: bool = False,
        api_base: str = LOCAL_API,
        page_timeout: float = 60.0,
    ) -> None:
        self.api_key = api_key
        self.profile_id = profile_id
        self.headless = headless
        self.cdp_mask = cdp_mask
        self.api_base = api_base.rstrip("/")
        self.page_timeout = page_timeout
        self.debug_port: int | None = None
        self._cdp: _CDP | None = None
        self._target_id: str | None = None

    # -- HTTP 到本地 API ------------------------------------------------

    def _api(self, path: str, timeout: float = 180.0) -> dict:
        req = urllib.request.Request(
            f"{self.api_base}{path}",
            headers={"Authorization": f"Bearer {self.api_key}"},
        )
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return json.loads(r.read().decode())

    def _api_post(self, path: str, payload: dict, timeout: float = 180.0) -> dict:
        data = json.dumps(payload).encode()
        req = urllib.request.Request(
            f"{self.api_base}{path}", data=data, method="POST",
            headers={"Authorization": f"Bearer {self.api_key}",
                     "Content-Type": "application/json"},
        )
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return json.loads(r.read().decode())

    # -- 生命周期 -------------------------------------------------------

    def active_port(self) -> int | None:
        """若该 profile 已在运行，返回调试端口。"""
        try:
            d = self._api("/api/v1/browser/local-active", timeout=15)
        except Exception:  # noqa: BLE001
            return None
        for item in (d.get("data") or {}).get("list") or []:
            if item.get("user_id") == self.profile_id:
                try:
                    return int(item.get("debug_port"))
                except (TypeError, ValueError):
                    return None
        return None

    def start(self, timeout: float = 180.0) -> int:
        """
        启动 profile 并返回 CDP 端口。

        注意：**不要用 CLI 的 `ads open-browser`**，它在内核已安装时
        仍可能误报 "SunBrowser xxx is not ready"，直接走原始 API 更可靠。
        """
        port = self.active_port()
        if port:
            self.debug_port = port
            return port

        q = {
            "user_id": self.profile_id,
            "cdp_mask": "0" if not self.cdp_mask else "1",
            "headless": "1" if self.headless else "0",
        }
        d = self._api(
            "/api/v1/browser/start?" + urllib.parse.urlencode(q),
            timeout=timeout,
        )
        if d.get("code") != 0:
            raise RuntimeError(f"启动 profile 失败: {d.get('msg')}")
        port = (d.get("data") or {}).get("debug_port")
        if not port:
            raise RuntimeError(f"启动成功但没拿到 debug_port: {d}")
        self.debug_port = int(port)
        return self.debug_port

    def stop(self) -> None:
        self._cdp_close()
        try:
            self._api(f"/api/v1/browser/stop?user_id={self.profile_id}",
                      timeout=60)
        except Exception:  # noqa: BLE001
            pass
        self.debug_port = None

    # -- CDP 连接 -------------------------------------------------------

    def _cdp_close(self) -> None:
        if self._cdp is not None:
            self._cdp.close()
            self._cdp = None
        self._target_id = None

    def connect(self, retries: int = 30, ws_override: str | None = None) -> None:
        """
        连上该 profile 的某个 page target。

        ws_override: 跨机场景下由隧道给出的本机可用 ws 地址，
        其 host:port 是本机映射端口，不是 Mac 上的 debug_port。
        """
        port = self.debug_port or self.start()
        # 跨机：直接用隧道给的 base（形如 127.0.0.1:16000）
        host_port = f"127.0.0.1:{port}"
        if ws_override:
            u = urllib.parse.urlparse(ws_override)
            host_port = f"{u.hostname}:{u.port}"
        last: Exception | None = None
        for _ in range(retries):
            try:
                with urllib.request.urlopen(
                    f"http://{host_port}/json/list", timeout=5
                ) as r:
                    targets = json.loads(r.read().decode())
                pages = [t for t in targets if t.get("type") == "page"]
                # 避开 AdsPower 自己的启动页
                page = next(
                    (t for t in pages
                     if "adspower.net" not in (t.get("url") or "")),
                    pages[0] if pages else None,
                )
                if page is None:
                    raise RuntimeError("没有可用的 page target")
                self._cdp = _CDP(page["webSocketDebuggerUrl"],
                                 timeout=self.page_timeout)
                self._target_id = page["id"]
                self._cdp.send("Runtime.enable")
                self._cdp.send("Page.enable")
                return
            except Exception as e:  # noqa: BLE001
                last = e
                time.sleep(1.0)
        raise RuntimeError(f"连接 CDP 失败: {last}")

    def _ensure(self) -> _CDP:
        if self._cdp is None:
            self.connect()
        assert self._cdp is not None
        return self._cdp

    # -- 页面操作 -------------------------------------------------------

    def navigate(self, url: str, wait: float = 6.0) -> None:
        self._ensure().send("Page.navigate", {"url": url})
        time.sleep(wait)

    def eval(self, expr: str, await_promise: bool = True,
             timeout: float | None = None) -> Any:
        cdp = self._ensure()
        res = cdp.send("Runtime.evaluate", {
            "expression": expr,
            "returnByValue": True,
            "awaitPromise": await_promise,
        }, timeout=timeout)
        result = res.get("result") or {}
        if result.get("subtype") == "error":
            raise RuntimeError(f"页面内执行出错: {result.get('description')}")
        return result.get("value")

    # -- 页面内发请求 ---------------------------------------------------

    def request(
        self,
        method: str,
        url: str,
        payload: dict | None = None,
        headers: dict[str, str] | None = None,
        fetch_timeout_ms: int = 60000,
    ) -> AdsResponse:
        """
        在页面上下文里用 fetch() 发请求。

        这样做的好处：Cookie、TLS、UA、sec-ch-ua、Origin 全部由浏览器
        原生决定，不存在任何"手工拼请求导致指纹不一致"的可能。
        """
        hdrs = dict(headers or {})
        opts: dict[str, Any] = {
            "method": method,
            "credentials": "include",
            "headers": hdrs,
        }
        if payload is not None:
            opts["body"] = json.dumps(payload, ensure_ascii=False)
            opts["headers"] = {**hdrs, "Content-Type": "application/json"}

        script = (
            "(async()=>{"
            "try{"
            f"const r=await fetch({json.dumps(url)},{json.dumps(opts)});"
            "const t=await r.text();"
            "const h={};r.headers.forEach((v,k)=>{h[k]=v});"
            "return JSON.stringify({s:r.status,b:t,h});"
            "}catch(e){return JSON.stringify({s:0,b:'',h:{},e:String(e)})}"
            "})()"
        )
        raw = self.eval(script, timeout=fetch_timeout_ms / 1000 + 10)
        if not raw:
            return AdsResponse(0, "", {}, "空响应")
        try:
            d = json.loads(raw)
        except Exception:  # noqa: BLE001
            return AdsResponse(0, str(raw), {}, "响应无法解析")
        return AdsResponse(
            status=int(d.get("s") or 0),
            body=d.get("b") or "",
            headers=d.get("h") or {},
            error=d.get("e") or "",
        )

    def get(self, url: str, **kw: Any) -> AdsResponse:
        return self.request("GET", url, **kw)

    def post_json(self, url: str, payload: dict, **kw: Any) -> AdsResponse:
        return self.request("POST", url, payload=payload, **kw)

    # -- iframe 操作（Stripe 的 CardElement 在跨域 iframe 里）-----------

    def fill_stripe_frames(self, js: str) -> int:
        """
        在 Stripe 的 CardElement iframe 里执行 js，返回执行的 frame 数。

        ⚠️ 实测：Stripe 的 iframe 是 OOPIF（独立进程），
        `Page.getFrameTree` **不会列出它们**，只能看到主文档。
        必须用 `Target.getTargets` 发现，再 attach 到对应 target 执行。
        """
        cdp = self._ensure()
        done = 0
        try:
            res = cdp.send("Target.getTargets")
        except Exception:  # noqa: BLE001
            return 0

        for t in res.get("targetInfos") or []:
            if t.get("type") != "iframe":
                continue
            url = (t.get("url") or "")
            # 只需要卡片输入那个 iframe
            if "elements-inner-card" not in url:
                continue
            tid = t.get("targetId")
            if not tid:
                continue
            try:
                att = cdp.send("Target.attachToTarget",
                               {"targetId": tid, "flatten": True})
                sid = att.get("sessionId")
                if not sid:
                    continue
                # 必须确认这个 frame 里真的有 cardnumber 输入框，
                # 否则会误填到相邻的 Stripe iframe，并把已填好的值覆盖成空
                # （实测导致 postal 丢失 → "Your postal code is incomplete"）。
                probe_js = (
                    "!!document.querySelector('input[name=cardnumber],"
                    "input[autocomplete=cc-number]')"
                )
                probe = cdp.send_session(
                    sid, "Runtime.evaluate",
                    {"expression": probe_js, "returnByValue": True})
                if not ((probe.get("result") or {}).get("value")):
                    continue
                cdp.send_session(sid, "Runtime.evaluate",
                                 {"expression": js, "returnByValue": True})
                done += 1
                # 只填第一个匹配 frame，避免重复覆盖
                break
            except Exception:  # noqa: BLE001
                continue
        return done

    # -- cookie ---------------------------------------------------------

    def cookies(self) -> dict[str, str]:
        """
        读浏览器 cookie（**含 HttpOnly**）。

        ⚠️ 不能用 document.cookie：会话 cookie `hive-portal-production`
        是 HttpOnly，JS 读不到，会误判成"没有会话"。
        必须走 CDP 的 Network.getAllCookies。
        """
        out: dict[str, str] = {}
        try:
            cdp = self._ensure()
            res = cdp.send("Network.getAllCookies")
        except Exception:  # noqa: BLE001
            return out
        for c in res.get("cookies") or []:
            name = c.get("name")
            dom = c.get("domain") or ""
            if not name:
                continue
            # 只关心 deploy.com 域下的 cookie，避免把别站同名 cookie 混进来
            if not dom.endswith("deploy.com"):
                continue
            out[name] = c.get("value") or ""
        return out

    def current_url(self) -> str:
        return self.eval("location.href") or ""

    def wait_for_url(self, needle: str, timeout: float = 60.0) -> bool:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if needle in (self.current_url() or ""):
                return True
            time.sleep(1.0)
        return False


# --------------------------------------------------------------------------
# 便捷函数
# --------------------------------------------------------------------------

def open_remote_profile(api_key: str, profile_id: str,
                        api_base: str, tunnel_daemon: str,
                        **kw: Any) -> AdsProfile:
    """
    跨机模式：API 走隧道，CDP 走按需建的隧道。

    架构：
        OVH(本函数) ──► api_base(Mac 的 API，经 -R 隧道)
                    ──► tunnel_daemon(Mac 的守护，请求开 CDP 隧道)
                    ◄── ws://127.0.0.1:<local>/... (隧道已映射)

    profile 的 Cookie/本地存储都在 Mac 上，这里只驱动，不搬运。
    """
    p = AdsProfile(api_key, profile_id, api_base=api_base, **kw)
    p.api_base = api_base.rstrip("/")

    # 1. 启动（或复用）profile，拿到 Mac 侧 debug_port
    data = p._api(
        f"/api/v1/browser/start?user_id={profile_id}&cdp_mask=0&headless=0",
        timeout=240)
    if data.get("code") != 0:
        raise RuntimeError(f"启动失败: {data.get('msg')}")
    mac_port = int((data.get("data") or {}).get("debug_port") or 0)
    if not mac_port:
        raise RuntimeError(f"没有 debug_port: {data}")

    # 2. 请守护在本机侧开隧道
    q = urllib.parse.urlencode({"port": mac_port})
    req = urllib.request.Request(f"{tunnel_daemon.rstrip('/')}/tunnel?{q}",
                                 method="POST")
    with urllib.request.urlopen(req, timeout=90) as r:
        d = json.loads(r.read().decode())
    if not d.get("ok"):
        raise RuntimeError(f"隧道建立失败: {d.get('error')}")
    local_port = int(d.get("local_port") or 0)

    ws = ((data.get("data") or {}).get("ws") or {}).get("puppeteer") or ""
    ws = ws.replace(f"127.0.0.1:{mac_port}", f"127.0.0.1:{local_port}")

    p.debug_port = local_port
    p.connect(ws_override=ws)
    return p


def open_profile(api_key: str, profile_id: str, **kw: Any) -> AdsProfile:
    """一步启动 + 连接。"""
    p = AdsProfile(api_key, profile_id, **kw)
    p.start()
    p.connect()
    return p


if __name__ == "__main__":  # 冒烟测试
    import os
    import sys

    key = os.environ.get("ADS_API_KEY", "")
    pid = os.environ.get("ADS_PROFILE_ID", "")
    if not (key and pid):
        print("用法: ADS_API_KEY=... ADS_PROFILE_ID=... python3 ads_transport.py",
              file=sys.stderr)
        raise SystemExit(1)

    p = open_profile(key, pid)
    print("port:", p.debug_port, "url:", p.current_url()[:60])
    p.navigate("https://deploy.com/signup?source=landing_login", wait=8)
    print("now:", p.current_url()[:70])
    r = p.get("https://ajax-portal.deploy.com/api/csrf")
    print("csrf:", r.status, r.body[:80])
    print("ua:", p.eval("navigator.userAgent")[:80])
