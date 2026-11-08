package mistralcards

import (
	"strings"
	"testing"
	"time"
)

func TestParseLine_Valid(t *testing.T) {
	// 用户给的示例格式
	c, err := ParseLine("4633433883347213----05/28----913")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Number != "4633433883347213" {
		t.Errorf("number = %q", c.Number)
	}
	if c.ExpMonth != 5 || c.ExpYear != 2028 {
		t.Errorf("expiry = %d/%d, want 5/2028", c.ExpMonth, c.ExpYear)
	}
	if c.CVV != "913" {
		t.Errorf("cvv = %q", c.CVV)
	}
}

func TestParseLine_ExpiryVariants(t *testing.T) {
	cases := []struct {
		raw   string
		wantM int
		wantY int
	}{
		{"4242424242424242----5/28----123", 5, 2028},
		{"4242424242424242----05-28----123", 5, 2028},
		{"4242424242424242----0528----123", 5, 2028},
		{"4242424242424242----12/2030----123", 12, 2030},
		{"4242424242424242---- 05 / 28 ----123", 5, 2028},
	}
	for _, tc := range cases {
		c, err := ParseLine(tc.raw)
		if err != nil {
			t.Errorf("%q: unexpected error %v", tc.raw, err)
			continue
		}
		if c.ExpMonth != tc.wantM || c.ExpYear != tc.wantY {
			t.Errorf("%q: got %d/%d, want %d/%d",
				tc.raw, c.ExpMonth, c.ExpYear, tc.wantM, tc.wantY)
		}
	}
}

func TestParseLine_CardNumberSeparators(t *testing.T) {
	// 允许粘贴带空格或短横线的卡号
	for _, raw := range []string{
		"4242 4242 4242 4242----12/30----123",
		"4242-4242-4242-4242----12/30----123",
	} {
		c, err := ParseLine(raw)
		if err != nil {
			t.Errorf("%q: unexpected error %v", raw, err)
			continue
		}
		if c.Number != "4242424242424242" {
			t.Errorf("%q: number = %q", raw, c.Number)
		}
	}
}

func TestParseLine_Errors(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		hint string
	}{
		{"empty", "", "empty"},
		{"two fields", "4242424242424242----12/30", "3 fields"},
		{"four fields", "4242424242424242----12/30----123----extra", "3 fields"},
		{"luhn", "4633433883347214----05/28----913", "Luhn"},
		{"too short", "424242424242----12/30----123", "length"},
		{"bad month", "4242424242424242----13/28----123", "month"},
		{"bad year", "4242424242424242----05/xx----123", "year"},
		{"short cvv", "4242424242424242----05/28----91", "cvv"},
		{"long cvv", "4242424242424242----05/28----91234", "cvv"},
		{"non-digit number", "4242abcd42424242----05/28----123", "non-digits"},
	}
	for _, tc := range cases {
		_, err := ParseLine(tc.raw)
		if err == nil {
			t.Errorf("%s: expected error, got nil", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.hint) {
			t.Errorf("%s: error %q does not mention %q", tc.name, err, tc.hint)
		}
	}
}

func TestParseLines_Dedup(t *testing.T) {
	lines := []string{
		"4242424242424242----12/30----123",
		"",
		"4242424242424242----06/31----456", // 重复卡号
		"5555555555554444----08/29----789",
	}
	cards, errs := ParseLines(lines)
	if len(cards) != 2 {
		t.Fatalf("got %d cards, want 2", len(cards))
	}
	if len(errs) != 1 {
		t.Fatalf("got %d errors, want 1: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0].Reason, "duplicate") {
		t.Errorf("expected duplicate error, got %q", errs[0].Reason)
	}
	if errs[0].Line != 3 {
		t.Errorf("error line = %d, want 3", errs[0].Line)
	}
}

func TestParseLines_KeepsGoingAfterError(t *testing.T) {
	lines := []string{
		"garbage",
		"4242424242424242----12/30----123",
		"also bad",
		"5555555555554444----08/29----789",
	}
	cards, errs := ParseLines(lines)
	if len(cards) != 2 {
		t.Fatalf("got %d cards, want 2", len(cards))
	}
	if len(errs) != 2 {
		t.Fatalf("got %d errors, want 2", len(errs))
	}
}

func TestExpired(t *testing.T) {
	now := time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		month, year int
		want        bool
	}{
		{5, 2026, false}, // 当月仍有效
		{6, 2026, false},
		{4, 2026, true}, // 上月已过期
		{12, 2025, true},
		{1, 2027, false},
	}
	for _, tc := range cases {
		c := Card{ExpMonth: tc.month, ExpYear: tc.year}
		if got := c.Expired(now); got != tc.want {
			t.Errorf("%02d/%d expired = %v, want %v",
				tc.month, tc.year, got, tc.want)
		}
	}
}

func TestMaskCard_NoPlaintext(t *testing.T) {
	c := Card{Number: "4633433883347213", ExpMonth: 5, ExpYear: 2028}
	m := MaskCard(c)
	if strings.Contains(m, "4633433883347213") {
		t.Errorf("mask leaked full number: %q", m)
	}
	if !strings.HasPrefix(m, "463343") {
		t.Errorf("mask = %q, want BIN prefix preserved", m)
	}
	if !strings.HasSuffix(m, "7213") {
		t.Errorf("mask = %q, want last4 preserved", m)
	}
}

func TestMaskLine_Malformed(t *testing.T) {
	if got := MaskLine("garbage-no-separators"); got != "***malformed***" {
		t.Errorf("masked malformed line = %q", got)
	}
}

func TestExpiry(t *testing.T) {
	c := Card{ExpMonth: 5, ExpYear: 2028}
	if got := c.Expiry(); got != "05/28" {
		t.Errorf("Expiry() = %q, want 05/28", got)
	}
	c = Card{ExpMonth: 12, ExpYear: 2030}
	if got := c.Expiry(); got != "12/30" {
		t.Errorf("Expiry() = %q, want 12/30", got)
	}
}

func TestLuhn(t *testing.T) {
	valid := []string{
		"4242424242424242",
		"5555555555554444",
		"378282246310005",
		"4633433883347213",
	}
	for _, n := range valid {
		if !luhnValid(n) {
			t.Errorf("%s should be Luhn-valid", n)
		}
	}
	if luhnValid("4242424242424241") {
		t.Error("4242424242424241 should fail Luhn")
	}
}
