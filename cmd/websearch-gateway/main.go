// websearch-gateway is an independent, opt-in Responses search compatibility service.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"account-pool-balancer/internal/searchgateway"
)

func main() {
	if err := run(); err != nil {
		slog.Error("websearch gateway stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	config := flag.String("config", "/config/gateway.json", "Gateway JSON configuration (environment variable references only)")
	check := flag.Bool("check", false, "Validate configuration and exit without network calls")
	health := flag.String("healthcheck", "", "Request this local healthz URL and exit")
	flag.Parse()
	if *health != "" {
		client := http.Client{Timeout: 3 * time.Second}
		resp, err := client.Get(*health)
		if err != nil {
			return fmt.Errorf("health check failed")
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode != 200 {
			return fmt.Errorf("health check failed")
		}
		return nil
	}
	f, err := os.Open(*config)
	if err != nil {
		return fmt.Errorf("cannot open gateway config")
	}
	defer f.Close()
	c, err := searchgateway.ReadConfig(f, os.Getenv)
	if err != nil {
		return err
	}
	if *check {
		slog.Info("gateway configuration valid")
		return nil
	}
	g, err := searchgateway.New(c, searchgateway.Options{Logger: slog.Default()})
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	server := &http.Server{Addr: c.Listen, Handler: g, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: time.Duration(c.RequestTimeoutSec+5) * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10, BaseContext: func(net.Listener) context.Context { return ctx }}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	select {
	case err := <-done:
		if err != http.ErrServerClosed {
			return fmt.Errorf("gateway listen failed")
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
		}
		<-done
	}
	return nil
}
