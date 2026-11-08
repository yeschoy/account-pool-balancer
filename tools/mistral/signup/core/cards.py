"""
银行卡列表（Python 侧）

与 Go 端 internal/mistralcards 保持同一套规则：

    格式：卡号----有效期----CVV
          4633433883347213----05/28----913

    校验：卡号 13-19 位 + Luhn；有效期支持 05/28 / 5/2028 / 05-28 / 0528；
         CVV 3-4 位

为什么两边都要实现：
    Go 侧负责页面校验与展示（保存时立即给反馈），
    Python 侧负责实际使用（注册/订阅时取值）。
    规则必须一致，否则页面显示"通过"但 worker 用不了。
"""
from __future__ import annotations

import os
import re
from dataclasses import dataclass
from datetime import datetime
from typing import Optional

SEPARATOR = "----"


@dataclass
class Card:
    number: str      # 纯数字
    exp_month: int   # 1-12
    exp_year: int    # 四位
    cvv: str

    # ---------- 派生出 Stripe 需要的字段 ----------
    @property
    def expiry(self) -> str:
        """MM/YY"""
        return f"{self.exp_month:02d}/{self.exp_year % 100:02d}"

    @property
    def bin(self) -> str:
        return self.number[:6]

    @property
    def last4(self) -> str:
        return self.number[-4:]

    def masked(self) -> str:
        n = self.number
        if len(n) <= 10:
            return "****"
        return f"{n[:6]}{'*' * (len(n) - 10)}{n[-4:]}"

    def expired(self, now: Optional[datetime] = None) -> bool:
        now = now or datetime.now()
        if self.exp_year < now.year:
            return True
        if self.exp_year == now.year and self.exp_month < now.month:
            return True
        return False

    def as_line(self) -> str:
        """还原成输入格式，便于透传"""
        return f"{self.number}{SEPARATOR}{self.expiry}{SEPARATOR}{self.cvv}"

    def as_stripe_dict(self) -> dict:
        """Stripe /v1/payment_methods 需要的字段"""
        return {
            "card[number]": self.number,
            "card[exp_month]": str(self.exp_month),
            "card[exp_year]": str(self.exp_year),
            "card[cvc]": self.cvv,
        }

    def __str__(self) -> str:
        return f"{self.masked()} exp={self.expiry} cvv={'*' * len(self.cvv)}"


# ---------------------------------------------------------------- 解析
def _digits(s: str) -> str:
    return "".join(ch for ch in s if ch.isdigit())


def _luhn_ok(number: str) -> bool:
    total = 0
    double = False
    for ch in reversed(number):
        d = ord(ch) - 48
        if double:
            d *= 2
            if d > 9:
                d -= 9
        total += d
        double = not double
    return total % 10 == 0


def _parse_expiry(raw: str) -> tuple[int, int]:
    """返回 (月, 四位年)，失败抛 ValueError"""
    s = raw.strip().replace("-", "/")
    if not s:
        raise ValueError("expiry is empty")

    if "/" in s:
        parts = s.split("/")
        if len(parts) != 2:
            raise ValueError(f"bad expiry {raw!r}")
        mm_s, yy_s = parts[0].strip(), parts[1].strip()
    else:
        d = _digits(s)
        if len(d) != 4:
            raise ValueError(f"bad expiry {raw!r}")
        mm_s, yy_s = d[:2], d[2:]

    try:
        month = int(mm_s)
    except ValueError:
        raise ValueError(f"bad expiry month {mm_s!r}")
    if not 1 <= month <= 12:
        raise ValueError(f"bad expiry month {mm_s!r}")

    try:
        year = int(yy_s)
    except ValueError:
        raise ValueError(f"bad expiry year {yy_s!r}")

    if len(yy_s) == 2:
        year += 2000
    elif len(yy_s) != 4:
        raise ValueError(f"bad expiry year {yy_s!r}")

    return month, year


def parse_line(line: str) -> Card:
    """解析单行，格式错误抛 ValueError"""
    raw = (line or "").strip()
    if not raw:
        raise ValueError("empty line")

    parts = raw.split(SEPARATOR)
    if len(parts) != 3:
        raise ValueError(
            f"expected 3 fields separated by {SEPARATOR!r}, got {len(parts)}")

    num_raw, exp_raw, cvv_raw = (p.strip() for p in parts)

    number = _digits(num_raw)
    if not number:
        raise ValueError("card number is empty")

    # 允许粘贴带空格/短横线的卡号
    stripped = num_raw.replace(" ", "").replace("-", "").replace("\t", "")
    if not stripped.isdigit():
        raise ValueError("card number contains non-digits")
    if not 13 <= len(number) <= 19:
        raise ValueError(f"card number length {len(number)} out of range 13-19")
    if not _luhn_ok(number):
        raise ValueError("card number fails Luhn check")

    month, year = _parse_expiry(exp_raw)

    cvv = _digits(cvv_raw)
    if not cvv:
        raise ValueError("cvv is empty")
    if not 3 <= len(cvv) <= 4:
        raise ValueError(f"cvv length {len(cvv)} out of range 3-4")

    return Card(number=number, exp_month=month, exp_year=year, cvv=cvv)


def parse_lines(lines: list[str]) -> tuple[list[Card], list[dict]]:
    """
    批量解析。返回 (卡列表, 错误列表)。
    空行静默跳过；重复卡号记为错误（保留首次出现的）。
    """
    cards: list[Card] = []
    errors: list[dict] = []
    seen: dict[str, int] = {}

    for i, line in enumerate(lines, 1):
        if not (line or "").strip():
            continue
        try:
            c = parse_line(line)
        except ValueError as e:
            errors.append({"line": i, "reason": str(e)})
            continue
        if c.number in seen:
            errors.append({
                "line": i,
                "reason": f"duplicate card (first seen at line {seen[c.number]})",
            })
            continue
        seen[c.number] = i
        cards.append(c)

    return cards, errors


# ---------------------------------------------------------------- 环境变量
def cards_from_env() -> list[Card]:
    """
    读取 MISTRAL_CARDS 环境变量（Go 侧注入，换行分隔）。
    Go 侧已经过滤过无效行，这里再解析一次是防御性的：
    格式不符的行直接跳过，不中断注册。
    """
    raw = os.environ.get("MISTRAL_CARDS", "").strip()
    if not raw:
        return []
    cards, _ = parse_lines(raw.split("\n"))
    return cards


def pick_card(cards: list[Card], index: int) -> Optional[Card]:
    """
    按序号轮转选卡，与 Go 侧 PickCardByIndex 的 n % len 规则一致。
    """
    if not cards:
        return None
    return cards[index % len(cards)] if index >= 0 else cards[0]


def pick_unused_card(cards: list[Card], used: set[str]) -> Optional[Card]:
    """
    选第一张未使用过的卡（按卡号判断）。用于一卡一号场景。
    used 由调用方维护（例如从已完成的候选列表里收集）。
    """
    for c in cards:
        if c.number not in used:
            return c
    return None
