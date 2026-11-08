"""
银行卡解析测试

重点验证与 Go 端 internal/mistralcards 的规则一致性：
同样的输入必须得到同样的结果，否则页面显示"通过"但 worker 用不了。
"""
from __future__ import annotations

import sys
from datetime import datetime
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent / "signup"))

from core.cards import (  # noqa: E402
    Card, cards_from_env, parse_line, parse_lines, pick_card, pick_unused_card,
)


# ---------------------------------------------------------------- 基本
def test_user_sample():
    """用户提供的真实示例"""
    c = parse_line("4633433883347213----05/28----913")
    assert c.number == "4633433883347213"
    assert c.exp_month == 5
    assert c.exp_year == 2028
    assert c.cvv == "913"
    assert c.expiry == "05/28"
    assert c.bin == "463343"
    assert c.last4 == "7213"


def test_expiry_variants():
    cases = [
        ("4242424242424242----5/28----123", 5, 2028),
        ("4242424242424242----05-28----123", 5, 2028),
        ("4242424242424242----0528----123", 5, 2028),
        ("4242424242424242----12/2030----123", 12, 2030),
        ("4242424242424242---- 05 / 28 ----123", 5, 2028),
    ]
    for raw, m, y in cases:
        c = parse_line(raw)
        assert (c.exp_month, c.exp_year) == (m, y), f"{raw} -> {c.exp_month}/{c.exp_year}"


def test_number_separators():
    for raw in [
        "4242 4242 4242 4242----12/30----123",
        "4242-4242-4242-4242----12/30----123",
    ]:
        assert parse_line(raw).number == "4242424242424242"


def test_errors():
    cases = [
        ("", "empty"),
        ("4242424242424242----12/30", "3 fields"),
        ("4242424242424242----12/30----123----x", "3 fields"),
        ("4633433883347214----05/28----913", "Luhn"),
        ("424242424242----12/30----123", "length"),
        ("4242424242424242----13/28----123", "month"),
        ("4242424242424242----05/xx----123", "year"),
        ("4242424242424242----05/28----91", "cvv"),
        ("4242424242424242----05/28----91234", "cvv"),
        ("4242abcd42424242----05/28----123", "non-digits"),
    ]
    for raw, hint in cases:
        try:
            parse_line(raw)
        except ValueError as e:
            assert hint in str(e), f"{raw!r}: {e} 不含 {hint!r}"
        else:
            raise AssertionError(f"{raw!r} 应该报错")


def test_dedup():
    cards, errs = parse_lines([
        "4242424242424242----12/30----123",
        "",
        "4242424242424242----06/31----456",
        "5555555555554444----08/29----789",
    ])
    assert len(cards) == 2
    assert len(errs) == 1
    assert "duplicate" in errs[0]["reason"]
    assert errs[0]["line"] == 3


def test_keeps_going_after_error():
    cards, errs = parse_lines([
        "garbage",
        "4242424242424242----12/30----123",
        "also bad",
        "5555555555554444----08/29----789",
    ])
    assert len(cards) == 2
    assert len(errs) == 2


# ---------------------------------------------------------------- 过期
def test_expired():
    now = datetime(2026, 5, 15)
    cases = [
        (5, 2026, False),   # 当月仍有效
        (6, 2026, False),
        (4, 2026, True),    # 上月已过期
        (12, 2025, True),
        (1, 2027, False),
    ]
    for m, y, want in cases:
        c = Card(number="4242424242424242", exp_month=m, exp_year=y, cvv="123")
        assert c.expired(now) is want, f"{m:02d}/{y} expired 应为 {want}"


# ---------------------------------------------------------------- 脱敏
def test_masking():
    c = parse_line("4633433883347213----05/28----913")
    m = c.masked()
    assert "4633433883347213" not in m, "脱敏泄露完整卡号"
    assert m.startswith("463343")
    assert m.endswith("7213")
    s = str(c)
    assert "913" not in s.replace("***", ""), "脱敏泄露 CVV"


def test_as_line_roundtrip():
    raw = "4633433883347213----05/28----913"
    assert parse_line(raw).as_line() == raw


def test_stripe_dict():
    c = parse_line("4633433883347213----05/28----913")
    d = c.as_stripe_dict()
    assert d["card[number]"] == "4633433883347213"
    assert d["card[exp_month]"] == "5"
    assert d["card[exp_year]"] == "2028"
    assert d["card[cvc]"] == "913"


# ---------------------------------------------------------------- 环境变量
def test_cards_from_env(monkeypatch=None):
    import os
    old = os.environ.get("MISTRAL_CARDS")
    try:
        os.environ["MISTRAL_CARDS"] = (
            "4633433883347213----05/28----913\n"
            "4242424242424242----12/30----123\n"
            "bad-line\n"
        )
        cards = cards_from_env()
        # Go 侧已过滤，Python 侧防御性跳过坏行
        assert len(cards) == 2
        assert cards[0].last4 == "7213"
    finally:
        if old is None:
            os.environ.pop("MISTRAL_CARDS", None)
        else:
            os.environ["MISTRAL_CARDS"] = old


def test_empty_env():
    import os
    old = os.environ.get("MISTRAL_CARDS")
    try:
        os.environ.pop("MISTRAL_CARDS", None)
        assert cards_from_env() == []
    finally:
        if old is not None:
            os.environ["MISTRAL_CARDS"] = old


# ---------------------------------------------------------------- 轮转
def test_pick_card_rotation():
    cards, _ = parse_lines([
        "4242424242424242----12/30----123",
        "5555555555554444----08/29----789",
    ])
    # 与 Go 侧 n % len 规则一致
    assert pick_card(cards, 0).last4 == "4242"
    assert pick_card(cards, 1).last4 == "4444"
    assert pick_card(cards, 2).last4 == "4242"
    assert pick_card(cards, 99).last4 == "4444"
    assert pick_card([], 0) is None


def test_pick_unused():
    cards, _ = parse_lines([
        "4242424242424242----12/30----123",
        "5555555555554444----08/29----789",
    ])
    assert pick_unused_card(cards, set()).last4 == "4242"
    assert pick_unused_card(cards, {"4242424242424242"}).last4 == "4444"
    assert pick_unused_card(cards, {"4242424242424242", "5555555555554444"}) is None


# ---------------------------------------------------------------- 与 Go 一致性
def test_consistent_with_go():
    """
    关键：这里列出的输入/结果必须与 Go 侧 cards_test.go 完全一致。
    如果哪天改了规则，两边测试会同时失败。
    """
    # (输入, 是否通过)
    same_cases = [
        ("4633433883347213----05/28----913", True),
        ("4242424242424242----5/28----123", True),
        ("4242424242424242----0528----123", True),
        ("4242424242424242----12/2030----123", True),
        ("4633433883347214----05/28----913", False),  # Luhn
        ("424242424242----12/30----123", False),      # 12 位太短
        ("4242424242424242----13/28----123", False),  # 月份
        ("4242424242424242----05/28----91", False),   # CVV 太短
    ]
    for raw, should_pass in same_cases:
        try:
            parse_line(raw)
            got = True
        except ValueError:
            got = False
        assert got is should_pass, f"{raw}: python={got}, 期望={should_pass}"


if __name__ == "__main__":
    import inspect
    import traceback

    fns = [(n, f) for n, f in sorted(globals().items())
           if n.startswith("test_") and callable(f)]
    passed = failed = 0
    for name, fn in fns:
        try:
            sig = inspect.signature(fn)
            fn() if not sig.parameters else fn(None)
            print(f"  PASS  {name}")
            passed += 1
        except Exception:
            print(f"  FAIL  {name}")
            traceback.print_exc()
            failed += 1
    print(f"\n{passed} passed, {failed} failed")
    sys.exit(1 if failed else 0)
