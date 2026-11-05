package pool

import (
	"errors"
	"math"
	"time"
)

const recentRequestMinutes = 60

// RequestMinute is a sparse bucket of attempts whose upstream headers were
// sent. The window is minute-granular: current minute plus the previous 59.
type RequestMinute struct {
	Minute   int64 `json:"minute"`
	Requests int64 `json:"requests"`
}

func (c *Counter) recentCount(now time.Time) int64 {
	minute := now.Unix() / 60
	var n int64
	for _, b := range c.RecentRequests {
		if b.Minute > minute-recentRequestMinutes && b.Minute <= minute {
			if b.Requests > math.MaxInt64-n {
				return math.MaxInt64
			}
			n += b.Requests
		}
	}
	return n
}

func (c *Counter) recentSnapshot(now time.Time) []RequestMinute {
	minute := now.Unix() / 60
	var out []RequestMinute
	for _, b := range c.RecentRequests {
		if b.Minute > minute-recentRequestMinutes && b.Minute <= minute {
			out = append(out, b)
		}
	}
	return out
}

func (c *Counter) recordRequest(now time.Time) {
	minute := now.Unix() / 60
	kept := c.RecentRequests[:0]
	for _, b := range c.RecentRequests {
		if b.Minute > minute-recentRequestMinutes && b.Minute <= minute {
			kept = append(kept, b)
		}
	}
	c.RecentRequests = kept
	if c.recentCount(now) == math.MaxInt64 {
		return
	}
	if n := len(kept); n > 0 && kept[n-1].Minute == minute {
		c.RecentRequests[n-1].Requests++
	} else {
		c.RecentRequests = append(kept, RequestMinute{Minute: minute, Requests: 1})
	}
}

func validateRecentRequests(buckets []RequestMinute) error {
	if len(buckets) > recentRequestMinutes {
		return errors.New("more than 60 minute buckets")
	}
	last, total := int64(-1), int64(0)
	for _, b := range buckets {
		if b.Minute < 0 || b.Minute <= last || b.Requests <= 0 || b.Requests > math.MaxInt64-total {
			return errors.New("invalid, duplicate, unordered or overflowing minute bucket")
		}
		last, total = b.Minute, total+b.Requests
	}
	return nil
}
