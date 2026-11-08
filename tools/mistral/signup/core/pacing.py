"""
节奏控制（pacing）

为什么需要：
    注册机默认会在几秒内连续完成 注册 → 登录 → 接受 ToS → 建三把 Key。
    真人不会这样。Mistral 侧的 Cloudflare 与 Stripe Radar 都会看：
      - 同一会话内的动作间隔
      - 建 Key 的连发密度
      - 注册到订阅的间隔

    实测真人节奏参考（抓包 2026-11-07，shellac-visitor.3）：
      注册成功 → 首次打开 admin  ≈ 5 分钟
      订阅页停留                ≈ 40 秒
      建 Key                    ≈ 手点，每把 5-15 秒

设计原则：
    1. 每个阶段边界都插入停顿，而不是只在开头 sleep 一次
    2. 停顿带随机抖动（对数正态），避免固定间隔本身成为特征
    3. 支持通过环境变量整体缩放，便于测试与生产用不同档位
    4. 关键节点（注册→登录）停顿更长，日常操作（建 Key）稍短
"""
from __future__ import annotations

import math
import os
import random
import time
from dataclasses import dataclass


def _env_scale() -> float:
    """MISTRAL_PACE_SCALE：整体缩放，默认 1.0。测试可设 0 关掉等待。"""
    raw = os.environ.get("MISTRAL_PACE_SCALE", "").strip()
    if not raw:
        return 1.0
    try:
        v = float(raw)
    except ValueError:
        return 1.0
    return max(0.0, min(v, 10.0))


def _env_off() -> bool:
    return os.environ.get("MISTRAL_PACE_OFF", "").strip() in ("1", "true", "yes")


@dataclass
class PaceStep:
    """一次停顿的配置"""
    name: str
    low: float          # 最短秒数
    high: float         # 最长秒数
    # 是否在对数空间采样。动作间隔通常更接近对数正态分布，
    # 线性均匀分布会显得过于规整。
    lognormal: bool = True


# ---------------------------------------------------------------- 节奏表
# 数值来自实测真人节奏的下限附近，宁可偏慢。
STEPS: dict[str, PaceStep] = {
    # --- 阶段 A：注册 ---
    "register_start":   PaceStep("注册前停留",     3.0,   8.0),
    "after_register":   PaceStep("注册后缓冲",     6.0,  14.0),
    "before_send_code": PaceStep("发码前停留",     2.0,   5.0),
    "after_send_code":  PaceStep("发码后等邮件",   3.0,   8.0),
    "before_submit_code": PaceStep("填验证码前",   2.5,   7.0),
    "after_verify":     PaceStep("验证通过后",     5.0,  12.0),

    # --- 阶段 B：登录 + ToS ---
    # 注册到登录是关键跃迁，给足间隔
    "register_to_login": PaceStep("注册→登录",    20.0,  60.0),
    "after_login":       PaceStep("登录后落定",     4.0,  10.0),
    "before_studio_tos": PaceStep("Studio ToS 前",  3.0,   8.0),
    "after_studio_tos":  PaceStep("Studio ToS 后",  2.5,   6.0),
    "before_chat_act":   PaceStep("Chat 操作前",    3.0,   7.0),
    "after_chat_tos":    PaceStep("Chat ToS 后",    2.5,   6.0),
    "before_vibe_key":   PaceStep("建 Vibe Key 前", 3.0,   8.0),

    # --- 阶段 C：建 Key ---
    # 连续建三把是明显的机器特征，这里给较大的间隔
    "between_keys":      PaceStep("两把 Key 之间", 8.0,  25.0),
    "after_keys":        PaceStep("建完 Key 后",    2.0,   5.0),

    # --- 订阅（后续接入时使用） ---
    "before_billing":    PaceStep("填账单前",       4.0,  10.0),
    "before_card":       PaceStep("填卡前",         5.0,  12.0),
    "before_subscribe":  PaceStep("提交订阅前",     6.0,  15.0),
    "after_subscribe":   PaceStep("订阅完成后",     3.0,   8.0),
    # 注册到订阅整体间隔（真人约 5 分钟，这里给下限保护）
    "register_to_subscribe": PaceStep("注册→订阅", 180.0, 420.0),
}


class Pacer:
    """
    带日志与可关闭开关的节奏控制器。

        p = Pacer(debug=print)
        p.wait("after_register")
    """

    def __init__(self, scale: float | None = None, off: bool | None = None,
                 debug=None):
        self.scale = _env_scale() if scale is None else max(0.0, scale)
        self.off = _env_off() if off is None else off
        self._log = debug or (lambda *a, **k: None)
        self._history: list[tuple[str, float]] = []

    # ---------- 采样 ----------
    @staticmethod
    def _sample(step: PaceStep) -> float:
        if step.high <= step.low:
            return step.low
        if not step.lognormal:
            return random.uniform(step.low, step.high)
        # 对数空间采样：多数落在中位偏下，偶尔偏长
        lo, hi = math.log(step.low), math.log(step.high)
        return math.exp(random.uniform(lo, hi))

    # ---------- 等待 ----------
    def wait(self, name: str, extra: float = 0.0) -> float:
        """按节奏表停顿，返回实际秒数"""
        step = STEPS.get(name)
        if step is None:
            self._log(f"[pace] 未知节奏名 {name}，跳过")
            return 0.0

        seconds = self._sample(step) * self.scale + extra

        if self.off or seconds <= 0.05:
            self._log(f"[pace] {step.name}: 跳过")
            return 0.0

        self._log(f"[pace] {step.name}: {seconds:.1f}s")
        self._sleep(seconds)
        self._history.append((name, seconds))
        return seconds

    @staticmethod
    def _sleep(seconds: float):
        """
        分片睡眠，便于被信号打断时不至于吞掉整个长等待
        （register_to_subscribe 最长 7 分钟）。
        """
        end = time.monotonic() + seconds
        while True:
            remain = end - time.monotonic()
            if remain <= 0:
                return
            time.sleep(min(remain, 5.0))

    # ---------- 统计 ----------
    def total_waited(self) -> float:
        return sum(s for _, s in self._history)

    def summary(self) -> str:
        if not self._history:
            return "无停顿"
        parts = [f"{n}={s:.1f}s" for n, s in self._history]
        return f"共 {self.total_waited():.1f}s（{', '.join(parts)}）"


def from_env(debug=None) -> Pacer:
    return Pacer(debug=debug)
