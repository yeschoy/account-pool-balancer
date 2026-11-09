// Command odb is the account-pool-balancer: an OpenAI/Anthropic
// compatible proxy for new-api in front of a pool of AI upstream accounts,
// with a web console at /.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"account-pool-balancer/internal/server"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	budget, err := strconv.Atoi(env("ODB_TH_BUFFER_MB", "2048"))
	if err != nil {
		log.Fatal("invalid ODB_TH_BUFFER_MB")
	}
	bufferMB := flag.Int("th-buffer-mb", budget, "TokenHarbor shared input-buffer budget in MiB (also capped by GOMEMLIMIT)")
	addr := flag.String("addr", env("ODB_ADDR", ":8790"), "listen address")
	dataDir := flag.String("data", env("ODB_DATA_DIR", "./data"), "data directory (config, metadata, logs)")
	auditKeyID := flag.String("mistral-cache-audit-key-id", env("ODB_MISTRAL_CACHE_AUDIT_KEY_ID", ""), "opt-in Mistral cache fingerprints for one enabled client key ID (30 minutes / 128 requests; no bodies)")
	flag.Parse()

	srv, err := server.New(*dataDir, server.Options{AdminPassword: os.Getenv("ODB_ADMIN_PASSWORD"), TokenHarborBufferedMB: *bufferMB, MistralCacheAuditKeyID: *auditKeyID})
	if err != nil {
		log.Fatalf("startup: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			if err := srv.Reload(); err != nil {
				log.Printf("reload: %v", err)
			} else {
				log.Printf("config reloaded")
			}
		}
	}()

	done := make(chan struct{})
	go func() { srv.Run(ctx); close(done) }()

	hs := &http.Server{Addr: *addr, Handler: srv.Handler(), ReadHeaderTimeout: 30 * time.Second}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = hs.Shutdown(shut)
	}()
	log.Printf("odb listening on %s, data dir %s", *addr, *dataDir)
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("listen: %v", err)
	}
	<-done
}
