package server

import (
	"math"
	"time"
)

// applyAverageTiming follows NewAPI's integer Unix-second difference, not a
// millisecond duration rounded down. This gateway's clock includes its admission,
// first-response wait and retries; it is not NewAPI's exact observation point.
func applyAverageTiming(rec *requestLog, ended time.Time) {
	if rec.TS.IsZero() {
		return
	}
	seconds := ended.Unix() - rec.TS.Unix()
	if seconds < 0 {
		return
	}
	rec.UseTimeSec = &seconds
	if rec.Result != "success" || seconds == 0 || rec.OutputTokens == nil || *rec.OutputTokens <= 0 {
		return
	}
	rate := float64(*rec.OutputTokens) / float64(seconds)
	rec.AverageTPS = &rate
}

// applyUpstreamTiming describes the final actual upstream attempt, not request
// upload, account admission, prior retries, or downstream postprocessing. The
// streaming value is an observed post-first-event average, not a provider's
// internal decode speed. Nonstreaming explicitly includes upstream first-byte
// wait. Missing usage and invalid/zero time windows remain unknown.
func applyUpstreamTiming(rec *requestLog, res *attemptResult) {
	if res.upstreamStarted.IsZero() || !res.upstreamLast.After(res.upstreamStarted) {
		return
	}
	total := res.upstreamLast.Sub(res.upstreamStarted)
	ms := total.Milliseconds()
	rec.UpstreamDurationMs = &ms
	if rec.Result != "success" || res.usage.out == nil || *res.usage.out < 0 {
		return
	}
	window, basis := total, "request-average"
	if res.upstreamStream {
		if res.upstreamFirst.Before(res.upstreamStarted) || !res.upstreamLast.After(res.upstreamFirst) || res.upstreamFirst.IsZero() {
			return
		}
		window, basis = res.upstreamLast.Sub(res.upstreamFirst), "post-first-event"
	}
	rate := float64(*res.usage.out) / window.Seconds()
	if math.IsNaN(rate) || math.IsInf(rate, 0) {
		return
	}
	outputMS := window.Milliseconds()
	rec.UpstreamOutputMs, rec.UpstreamTPSBasis = &outputMS, basis
	rate = math.Round(rate*100) / 100
	rec.UpstreamTPS = &rate
}
