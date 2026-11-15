// Command x50reg registers 50x AI accounts from iCloud HME mailboxes.
//
//	# one account
//	x50reg -mail "addr----https://icloud.mida.vip/messages/...?token=..."
//
//	# batch from a file, 4 at a time, rotating the proxy pool
//	x50reg -mailfile mailboxes.txt -proxies proxies.txt -c 4 -out accounts.jsonl
//
//	# mint N throwaway addresses programmatically
//	x50reg -generated 10 -out accounts.jsonl
//
// The tool is a plain HTTP client for the documented signup endpoints; it does
// not solve captchas and does not touch the signup page's consent switches.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"account-pool-balancer/internal/x50reg"
)

func main() {
	var (
		mailFlag  = flag.String("mail", "", "single mailbox: email----share_url, or -- - to read stdin")
		mailFile  = flag.String("mailfile", "", "file of email----share_url lines")
		generated = flag.Int("generated", 0, "number of accounts to attempt without an explicit mailbox list")
		genFile   = flag.String("generated-mailfile", "", "file of bare email addresses to attempt")
		proxyFile = flag.String("proxies", "", "file of proxy URLs, one per line")
		proxyOne  = flag.String("proxy", "", "single proxy URL")
		conc      = flag.Int("c", 1, "concurrent registrations")
		outPath   = flag.String("out", "accounts.jsonl", "append results as JSONL here")
		keyName   = flag.String("key-name", "registrar", "API key name to create")
		mailWait  = flag.Duration("mail-wait", 240*time.Second, "max wait for the confirm mail")
		mailPoll  = flag.Duration("mail-poll", 3*time.Second, "mailbox poll interval")
		timeout   = flag.Duration("timeout", 30*time.Second, "per-request timeout")
		apiBase   = flag.String("api", x50reg.DefaultAPIBase, "API base URL")
		origin    = flag.String("origin", x50reg.DefaultOrigin, "web origin sent as Origin/Referer")
		mailBase  = flag.String("mailbase", x50reg.DefaultMailBase, "shared mailbox API base")
		ua        = flag.String("ua", x50reg.DefaultUserAgent, "User-Agent")
		dryRun    = flag.Bool("dry-run", false, "parse inputs and print the plan without registering")
		quiteList = flag.Bool("list", false, "print parsed mailboxes and exit")
	)
	flag.Parse()

	var mailboxes []x50reg.Mailbox
	collect := func(block string) {
		mb, err := x50reg.ParseMailboxList(block)
		if err != nil {
			fmt.Fprintf(os.Stderr, "parse: %v\n", err)
			return
		}
		mailboxes = append(mailboxes, mb...)
	}

	if *mailFlag != "" {
		block := *mailFlag
		if block == "-" {
			b, _ := os.ReadFile("/dev/stdin")
			block = string(b)
		}
		collect(block)
	}
	if *mailFile != "" {
		b, err := os.ReadFile(*mailFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read mailfile: %v\n", err)
			os.Exit(2)
		}
		collect(string(b))
	}
	if *genFile != "" {
		b, err := os.ReadFile(*genFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read generated-mailfile: %v\n", err)
			os.Exit(2)
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			mailboxes = append(mailboxes, x50reg.Mailbox{Email: line})
		}
	}
	if *generated > 0 {
		start := len(mailboxes)
		for i := 0; i < *generated; i++ {
			mailboxes = append(mailboxes, x50reg.Mailbox{Email: fmt.Sprintf("x50x%08d@example.invalid", start+i)})
		}
	}

	if len(mailboxes) == 0 {
		fmt.Fprintln(os.Stderr, "nothing to do: pass -mail, -mailfile or -generated")
		flag.Usage()
		os.Exit(2)
	}
	if *quiteList {
		for _, mb := range mailboxes {
			fmt.Printf("%s\t%s\n", mb.Email, mb.Token)
		}
		return
	}

	// Proxy pool
	var proxies []string
	if *proxyFile != "" {
		list, err := x50reg.LoadProxyFile(*proxyFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read proxies: %v\n", err)
			os.Exit(2)
		}
		proxies = append(proxies, list...)
	}
	if *proxyOne != "" {
		proxies = append(proxies, *proxyOne)
	}
	pool := x50reg.NewProxyPool(proxies)

	cfg := x50reg.BatchConfig{
		Mailboxes:    mailboxes,
		Proxies:      pool,
		Concurrency:  *conc,
		MailWait:     *mailWait,
		MailInterval: *mailPoll,
		Timeout:      *timeout,
		KeyName:      *keyName,
		APIBase:      *apiBase,
		Origin:       *origin,
		MailBase:     *mailBase,
		UserAgent:    *ua,
		OutPath:      *outPath,
		Tracer: func(f string, a ...any) {
			fmt.Printf("%s "+f+"\n", append([]any{time.Now().Format("15:04:05")}, a...)...)
		},
	}

	if *dryRun {
		fmt.Printf("mailboxes=%d proxies=%d concurrency=%d out=%s\n", len(mailboxes), pool.Len(), *conc, *outPath)
		for _, mb := range mailboxes {
			fmt.Printf("  %s token=%s...\n", mb.Email, short(mb.Token))
		}
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	start := time.Now()
	results, err := x50reg.RunBatch(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "batch: %v\n", err)
		os.Exit(1)
	}

	ok, fail := 0, 0
	for _, r := range results {
		if r.OK {
			ok++
		} else {
			fail++
		}
	}
	fmt.Printf("\n=== done in %s: %d ok, %d failed, %d total ===\n", time.Since(start).Round(time.Second), ok, fail, len(results))
	for _, r := range results {
		if r.OK && r.Account != nil {
			fmt.Printf("%s\t%s\t%s\n", r.Account.Email, r.Account.Password, r.Account.APIKey)
		}
	}
	if fail > 0 {
		os.Exit(1)
	}
	_ = json.Marshal
}

func short(s string) string {
	if len(s) <= 10 {
		return s
	}
	return s[:10]
}
