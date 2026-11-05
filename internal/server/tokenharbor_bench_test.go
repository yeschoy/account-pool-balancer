package server

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

// Measures the local native gateway + local mock/client, not upstream model
// throughput. Payload bytes are real; synthetic usage is not a TPM claim.
func BenchmarkTokenHarborGateway(b *testing.B) {
	for _, count := range []int{2, 1000} {
		for _, stream := range []bool{false, true} {
			b.Run(fmt.Sprintf("accounts=%d/sse=%t", count, stream), func(b *testing.B) { benchmarkTHGateway(b, count, stream) })
		}
	}
}

func benchmarkTHGateway(b *testing.B, count int, stream bool) {
	e := newTHEnv(b)
	e.discardCalls = true
	if stream {
		e.setHandler(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			for i := 0; i < 10; i++ {
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"mock\"}}]}\n\n")
				w.(http.Flusher).Flush()
				time.Sleep(5 * time.Millisecond)
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
		})
	}
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		for i := range c.Accounts {
			c.Accounts[i].MaxConc = 0
		}
		for i := len(c.Accounts); i < count; i++ {
			c.Accounts = append(c.Accounts, config.Account{ID: fmt.Sprintf("bench-%d", i), Provider: config.ProviderTokenHarbor, Group: config.TokenHarborGroup, RuntimeKey: fmt.Sprintf("thk_live_benchmark_%08d", i), Enabled: true})
		}
		return nil
	})
	if err != nil {
		b.Fatal(err)
	}
	seedTHCapabilities(e.s)
	tr := &http.Transport{MaxIdleConns: 512, MaxIdleConnsPerHost: 512, MaxConnsPerHost: 512, DisableCompression: true}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 30 * time.Second}
	body := bigBody()
	var workers, requests, failed, peakInput atomic.Int64
	latencies := make([]int64, b.N)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.SetParallelism(4)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		session := fmt.Sprintf("bench-session-%d", workers.Add(1))
		for pb.Next() {
			i := requests.Add(1) - 1
			start := time.Now()
			r, _ := http.NewRequest("POST", e.front.URL+"/v1/chat/completions", bytes.NewReader(body))
			r.Header.Set("Authorization", "Bearer downstream-th")
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-Session-Id", session)
			resp, err := client.Do(r)
			used := e.s.thBuffered.Load()
			for prev := peakInput.Load(); used > prev; prev = peakInput.Load() {
				if peakInput.CompareAndSwap(prev, used) {
					break
				}
			}
			if err != nil {
				failed.Add(1)
			} else {
				_, readErr := io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode != 200 || readErr != nil {
					failed.Add(1)
				}
			}
			latencies[i] = time.Since(start).Nanoseconds()
		}
	})
	b.StopTimer()
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	if len(latencies) > 0 {
		b.ReportMetric(float64(latencies[(len(latencies)-1)*95/100])/1e6, "p95-ms")
		b.ReportMetric(float64(latencies[(len(latencies)-1)*99/100])/1e6, "p99-ms")
	}
	b.ReportMetric(float64(peakInput.Load())/(1<<20), "observed-input-MiB")
	// End-of-run RSS includes this client, upstream mock, cached data and Go
	// heap retained from earlier sub-benchmarks. It is not peak/server-only RSS.
	if out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output(); err == nil {
		if kb, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64); err == nil {
			b.ReportMetric(kb/1024, "end-RSS-MiB")
		}
	}
	if n := failed.Load(); n != 0 {
		b.Fatalf("%d failed local requests", n)
	}
	if e.s.thBuffered.Load() != 0 {
		b.Fatal("input reservations leaked after completed requests")
	}
}
