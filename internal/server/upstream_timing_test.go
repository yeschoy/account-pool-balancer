package server

import (
	"testing"
	"time"
)

func TestUpstreamTPSUsesFinalAttemptTiming(t *testing.T) {
	start := time.Unix(100, 0)
	tokens := int64(100)
	floatPointer := func(v float64) *float64 { return &v }
	for _, tc := range []struct {
		name, result, basis string
		stream              bool
		first, last         time.Duration
		usage               *int64
		want                *float64
	}{
		{"stream", "success", "post-first-event", true, 2 * time.Second, 4 * time.Second, &tokens, floatPointer(50)},
		{"nonstream", "success", "request-average", false, 0, 4 * time.Second, &tokens, floatPointer(25)},
		{"unknown usage", "success", "", true, time.Second, 2 * time.Second, nil, nil},
		{"no output window", "success", "", true, time.Second, time.Second, &tokens, nil},
		{"failed stream", "failed", "", true, time.Second, 2 * time.Second, &tokens, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &requestLog{Result: tc.result, DurationMs: 60000}
			res := &attemptResult{upstreamStarted: start, upstreamLast: start.Add(tc.last), upstreamStream: tc.stream, usage: usage{out: tc.usage}}
			if tc.stream {
				res.upstreamFirst = start.Add(tc.first)
			}
			applyUpstreamTiming(r, res)
			if tc.want == nil {
				if r.UpstreamTPS != nil || r.UpstreamTPSBasis != "" {
					t.Fatal("invented throughput for missing usage/failure/zero window")
				}
			} else if r.UpstreamTPS == nil || *r.UpstreamTPS != *tc.want || r.UpstreamTPSBasis != tc.basis {
				t.Fatalf("used request/queue/retry duration instead of final attempt: %#v", r)
			}
		})
	}
}
