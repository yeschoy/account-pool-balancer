package server

import (
	"testing"
	"time"
)

func TestAverageTPSMatchesNewAPISeconds(t *testing.T) {
	tokens := int64(100)
	zero := int64(0)
	for _, tc := range []struct {
		name, result string
		start, end   time.Time
		out          *int64
		seconds      int64
		want         float64
		known        bool
	}{
		{"whole request includes wait and retry", "success", time.Unix(100, 900000000), time.Unix(104, 100000000), &tokens, 4, 25, true},
		{"crossed second in 200ms", "success", time.Unix(100, 900000000), time.Unix(101, 100000000), &tokens, 1, 100, true},
		{"zero seconds", "success", time.Unix(100, 100000000), time.Unix(100, 900000000), &tokens, 0, 0, false},
		{"missing usage", "success", time.Unix(100, 0), time.Unix(104, 0), nil, 4, 0, false},
		{"zero output like NewAPI", "success", time.Unix(100, 0), time.Unix(104, 0), &zero, 4, 0, false},
		{"failed", "failed", time.Unix(100, 0), time.Unix(104, 0), &tokens, 4, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &requestLog{TS: tc.start, Result: tc.result, OutputTokens: tc.out, DurationMs: tc.end.Sub(tc.start).Milliseconds()}
			applyAverageTiming(r, tc.end)
			if r.UseTimeSec == nil || *r.UseTimeSec != tc.seconds || (r.AverageTPS != nil) != tc.known {
				t.Fatalf("incorrect whole-request timing: %#v", r)
			}
			if tc.known && *r.AverageTPS != tc.want {
				t.Fatalf("used fractional seconds or removed TTFT/retry: %v", *r.AverageTPS)
			}
		})
	}
}

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
