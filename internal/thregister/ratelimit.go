package thregister

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"account-pool-balancer/internal/rules"
)

// RateLimitError stops registration work. It never instructs the caller to
// replay a POST or change identity/egress; restarting requires a human action.
type RateLimitError struct {
	RetryAfter time.Duration
	Reason     string
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("官网注册暂停：%s；至少等待 %s 后人工重新发起，未自动重试", e.Reason, e.RetryAfter.Round(time.Second))
}

func (e *RateLimitError) Unwrap() error { return ErrSessionRejected }

func registrationRateLimit(resp *http.Response, message string) error {
	lower := strings.ToLower(message)
	busy := false
	for _, phrase := range []string{"unusually high number of sign-ups", "too many sign-ups", "too many signup", "too many requests"} {
		busy = busy || strings.Contains(lower, phrase)
	}
	if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusServiceUnavailable && !(resp.StatusCode >= 400 && resp.Header.Get("Retry-After") != "") && !busy {
		return nil
	}
	wait := rules.CooldownAfter(&rules.ErrorRule{Reset: &rules.Reset{Header: "Retry-After", Format: "retry-after", Fallback: "5m"}}, resp.Header, "", time.Now(), 1)
	if wait <= 0 {
		wait = 5 * time.Minute
	}
	// An oversized positive delta must not wrap to a short fallback. Keep
	// the longest representable pause instead of silently shortening it.
	if raw := strings.TrimSpace(resp.Header.Get("Retry-After")); raw != "" && strings.Trim(raw, "0123456789") == "" {
		const longest = time.Duration(1<<63 - 1)
		seconds, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || seconds > uint64(longest/time.Second) {
			wait = longest
		}
	}
	if message == "" {
		message = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return &RateLimitError{RetryAfter: wait, Reason: compact([]byte(message))}
}
