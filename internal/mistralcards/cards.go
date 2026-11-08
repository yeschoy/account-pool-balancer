// Package mistralcards 解析并校验 Mistral 注册机使用的银行卡列表。
//
// 输入格式（一行一张卡，分隔符是四个短横线）：
//
//	卡号----有效期----CVV
//	4633433883347213----05/28----913
//
// 设计取舍：
//   - 只做格式与校验位检查，不联网、不落盘、不打日志明文卡号。
//   - 解析失败的行整行丢弃并给出原因，不阻塞其余行。
//   - 有效期统一转成 (月, 完整年份)，避免下游各自处理 2 位/4 位年份。
package mistralcards

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Separator 是字段分隔符。刻意选四个短横线，避免与卡号里的字符冲突。
const Separator = "----"

// Card 是一张解析后的银行卡。
type Card struct {
	Number   string // 纯数字卡号
	ExpMonth int    // 1-12
	ExpYear  int    // 四位年份
	CVV      string // 保持字符串，保留前导零
}

// ParseError 描述某一行解析失败的原因。
type ParseError struct {
	Line   int    // 行号，从 1 开始
	Raw    string // 原始行（脱敏后）
	Reason string
}

func (e ParseError) Error() string {
	return fmt.Sprintf("line %d: %s (%s)", e.Line, e.Reason, e.Raw)
}

// ParseLine 解析单行。字段不足、格式错误、校验位失败都会返回错误。
func ParseLine(line string) (Card, error) {
	raw := strings.TrimSpace(line)
	if raw == "" {
		return Card{}, fmt.Errorf("empty line")
	}

	parts := strings.Split(raw, Separator)
	if len(parts) != 3 {
		return Card{}, fmt.Errorf("expected 3 fields separated by %q, got %d",
			Separator, len(parts))
	}

	numRaw := strings.TrimSpace(parts[0])
	expRaw := strings.TrimSpace(parts[1])
	cvvRaw := strings.TrimSpace(parts[2])

	// ---- 卡号 ----
	number := digitsOnly(numRaw)
	if number == "" {
		return Card{}, fmt.Errorf("card number is empty")
	}
	// 允许用户粘贴带空格或短横线的卡号，但去掉这些分隔符后必须全为数字
	stripped := strings.NewReplacer(" ", "", "-", "", "\t", "").Replace(numRaw)
	if !isDigits(stripped) {
		return Card{}, fmt.Errorf("card number contains non-digits")
	}
	if n := len(number); n < 13 || n > 19 {
		return Card{}, fmt.Errorf("card number length %d out of range 13-19", n)
	}
	if !luhnValid(number) {
		return Card{}, fmt.Errorf("card number fails Luhn check")
	}

	// ---- 有效期 ----
	month, year, err := parseExpiry(expRaw)
	if err != nil {
		return Card{}, err
	}

	// ---- CVV ----
	cvv := digitsOnly(cvvRaw)
	if cvv == "" {
		return Card{}, fmt.Errorf("cvv is empty")
	}
	if len(cvv) < 3 || len(cvv) > 4 {
		return Card{}, fmt.Errorf("cvv length %d out of range 3-4", len(cvv))
	}

	return Card{Number: number, ExpMonth: month, ExpYear: year, CVV: cvv}, nil
}

// ParseLines 批量解析。返回成功解析的卡与逐行错误。
// 空行被静默跳过，不计入错误。
func ParseLines(lines []string) ([]Card, []ParseError) {
	cards := make([]Card, 0, len(lines))
	errs := make([]ParseError, 0)
	seen := make(map[string]int, len(lines)) // 卡号 -> 首次出现的行号

	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		c, err := ParseLine(line)
		if err != nil {
			errs = append(errs, ParseError{
				Line:   i + 1,
				Raw:    MaskLine(line),
				Reason: err.Error(),
			})
			continue
		}
		if first, dup := seen[c.Number]; dup {
			errs = append(errs, ParseError{
				Line:   i + 1,
				Raw:    MaskCard(c),
				Reason: fmt.Sprintf("duplicate card (first seen at line %d)", first),
			})
			continue
		}
		seen[c.Number] = i + 1
		cards = append(cards, c)
	}
	return cards, errs
}

// Expired 报告卡是否在给定的“现在”之前到期。
// 有效期按月末计算：2026-05 的卡在 2026-05-31 结束时仍然有效。
func (c Card) Expired(now time.Time) bool {
	y, m, _ := now.Date()
	// 当年当月仍有效
	if c.ExpYear < y {
		return true
	}
	if c.ExpYear == y && c.ExpMonth < int(m) {
		return true
	}
	return false
}

// Expiry 返回 "MM/YY" 形式，便于展示。
func (c Card) Expiry() string {
	return fmt.Sprintf("%02d/%02d", c.ExpMonth, c.ExpYear%100)
}

// String 返回脱敏表示，可安全写日志。
func (c Card) String() string {
	return fmt.Sprintf("%s exp=%s cvv=%s", MaskCard(c), c.Expiry(),
		strings.Repeat("*", len(c.CVV)))
}

// MaskCard 返回 "463343******7213 exp=05/28" 形式的脱敏串。
func MaskCard(c Card) string {
	n := c.Number
	if len(n) <= 10 {
		return "****"
	}
	return n[:6] + strings.Repeat("*", len(n)-10) + n[len(n)-4:]
}

// MaskLine 把原始行里的卡号部分脱敏，保留分隔符结构与 CVV 位置，
// 便于定位用户的输入错误而不泄露卡号。
func MaskLine(line string) string {
	parts := strings.Split(strings.TrimSpace(line), Separator)
	if len(parts) != 3 {
		return "***malformed***"
	}
	num := digitsOnly(parts[0])
	masked := "****"
	if len(num) > 10 {
		masked = num[:6] + strings.Repeat("*", len(num)-10) + num[len(num)-4:]
	}
	return strings.Join([]string{masked, strings.TrimSpace(parts[1]), "***"}, Separator)
}

// ---------------------------------------------------------------- 内部

// parseExpiry 接受 "05/28"、"5/2028"、"05-28"、"0528" 等写法。
func parseExpiry(s string) (int, int, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "-", "/"))
	if s == "" {
		return 0, 0, fmt.Errorf("expiry is empty")
	}

	var mmStr, yyStr string
	if strings.Contains(s, "/") {
		parts := strings.Split(s, "/")
		if len(parts) != 2 {
			return 0, 0, fmt.Errorf("bad expiry %q", s)
		}
		mmStr, yyStr = strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	} else {
		d := digitsOnly(s)
		if len(d) != 4 {
			return 0, 0, fmt.Errorf("bad expiry %q", s)
		}
		mmStr, yyStr = d[:2], d[2:]
	}

	month, err := strconv.Atoi(mmStr)
	if err != nil || month < 1 || month > 12 {
		return 0, 0, fmt.Errorf("bad expiry month %q", mmStr)
	}

	year, err := strconv.Atoi(yyStr)
	if err != nil || year < 0 {
		return 0, 0, fmt.Errorf("bad expiry year %q", yyStr)
	}
	switch len(yyStr) {
	case 2:
		year += 2000
	case 4:
		// 原样使用
	default:
		return 0, 0, fmt.Errorf("bad expiry year %q", yyStr)
	}

	return month, year, nil
}

// digitsOnly 去掉所有非数字字符。
func digitsOnly(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isDigits(s string) bool { return s != "" && digitsOnly(s) == s }

// luhnValid 校验 Luhn 校验位。
func luhnValid(number string) bool {
	sum := 0
	double := false
	for i := len(number) - 1; i >= 0; i-- {
		d := int(number[i] - '0')
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum%10 == 0
}
