"""
节奏控制测试

重点：
  1. 每个节奏步都在合理区间内
  2. 关闭开关真的不等待
  3. 缩放系数线性生效
  4. 采样分布不是均匀的（对数正态）
"""
from __future__ import annotations

import os
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent / "signup"))

from core.pacing import STEPS, Pacer, PaceStep  # noqa: E402


def test_all_steps_reasonable():
    """所有节奏步必须有界且下限不为负"""
    for name, step in STEPS.items():
        assert isinstance(step, PaceStep), name
        assert step.low >= 0, f"{name}: low={step.low}"
        assert step.high >= step.low, f"{name}: high<low"
        assert step.high <= 600, f"{name}: high={step.high} 过长"
        assert step.name, f"{name}: 缺少中文名"


def test_key_transitions_paced():
    """关键跃迁必须有实质停顿，防止注册→登录过快"""
    must_pace = [
        "after_register", "register_to_login", "after_login",
        "between_keys", "after_verify",
    ]
    for name in must_pace:
        assert name in STEPS, f"缺少节奏步 {name}"
        assert STEPS[name].low >= 2.0, f"{name} 下限过短"


def test_sample_in_range():
    """采样结果必须落在 [low, high]"""
    import math
    for name, step in STEPS.items():
        for _ in range(200):
            v = Pacer._sample(step)
            assert step.low - 1e-6 <= v <= step.high + 1e-6, \
                f"{name}: 采样 {v} 越界 [{step.low}, {step.high}]"


def test_sample_is_lognormal_not_uniform():
    """
    对数正态的中位数应显著低于算术中点。
    若实现退化成均匀分布，这个断言会失败。
    """
    step = PaceStep("t", 8.0, 25.0)
    vals = sorted(Pacer._sample(step) for _ in range(2000))
    median = vals[len(vals) // 2]
    midpoint = (8.0 + 25.0) / 2  # 16.5
    assert median < midpoint, \
        f"中位数 {median:.2f} 不显著低于中点 {midpoint}，疑似非对数采样"


def test_off_does_not_sleep():
    p = Pacer(off=True)
    n = p.wait("register_to_login")  # 表里是 180-420 秒
    assert n == 0.0
    assert p.total_waited() == 0.0


def test_zero_scale_does_not_sleep():
    p = Pacer(scale=0.0)
    n = p.wait("register_to_login")
    assert n == 0.0


def test_scale_linear():
    """缩放后时长应落在缩放后的区间内"""
    p = Pacer(scale=0.01)  # 100 倍缩短
    total = 0.0
    for _ in range(20):
        total += p.wait("between_keys")  # 8-25 秒 -> 0.08-0.25 秒
    # 20 次总时长上限 20*0.25=5s，下限 20*0.08=1.6s
    assert 1.0 <= total <= 6.0, f"缩放后总时长 {total:.2f}s 超出预期"


def test_unknown_step_is_noop():
    p = Pacer(off=True)
    assert p.wait("nonexistent_step") == 0.0


def test_env_off_takes_priority():
    old_off = os.environ.get("MISTRAL_PACE_OFF")
    old_scale = os.environ.get("MISTRAL_PACE_SCALE")
    try:
        os.environ["MISTRAL_PACE_OFF"] = "1"
        os.environ["MISTRAL_PACE_SCALE"] = "5"
        p = Pacer()  # 从环境读
        assert p.wait("after_register") == 0.0
    finally:
        for k, v in (("MISTRAL_PACE_OFF", old_off),
                     ("MISTRAL_PACE_SCALE", old_scale)):
            if v is None:
                os.environ.pop(k, None)
            else:
                os.environ[k] = v


def test_env_scale_parsed():
    old = os.environ.get("MISTRAL_PACE_SCALE")
    try:
        os.environ["MISTRAL_PACE_SCALE"] = "0.5"
        p = Pacer()
        assert abs(p.scale - 0.5) < 1e-9
        os.environ["MISTRAL_PACE_SCALE"] = "999"
        p = Pacer()
        assert p.scale == 10.0, "应被上限截断到 10"
        os.environ["MISTRAL_PACE_SCALE"] = "garbage"
        p = Pacer()
        assert p.scale == 1.0, "非法值应回落 1.0"
    finally:
        if old is None:
            os.environ.pop("MISTRAL_PACE_SCALE", None)
        else:
            os.environ["MISTRAL_PACE_SCALE"] = old


def test_summary():
    p = Pacer(off=True)
    assert p.summary() == "无停顿"


def test_no_duplicate_step_names():
    """节奏步的 key 唯一，中文名也尽量唯一（便于日志排查）"""
    names = [s.name for s in STEPS.values()]
    # 允许少量复用（如 after_chat_tos 被多处引用），但不应全部重复
    assert len(set(names)) >= len(names) * 0.8


if __name__ == "__main__":
    import inspect
    import traceback

    fns = [(n, f) for n, f in sorted(globals().items())
           if n.startswith("test_") and callable(f)]
    passed = failed = 0
    for name, fn in fns:
        try:
            fn()
            print(f"  PASS  {name}")
            passed += 1
        except Exception:
            print(f"  FAIL  {name}")
            traceback.print_exc()
            failed += 1
    print(f"\n{passed} passed, {failed} failed")
    sys.exit(1 if failed else 0)
