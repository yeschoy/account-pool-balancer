package server

import (
	"sync"
	"testing"
)

// Most package tests read the details log as soon as the response is read.
// Keep those reads deterministic, but exercise the real request/queue path
// for explicitly selected servers without changing global behavior mid-test.
var asyncDetailServers sync.Map

func init() {
	detailInline = func(s *Server) bool {
		_, async := asyncDetailServers.Load(s)
		return !async
	}
}

func useAsyncDetails(t *testing.T, s *Server) {
	t.Helper()
	asyncDetailServers.Store(s, true)
	t.Cleanup(func() {
		s.closeDetails()
		asyncDetailServers.Delete(s)
	})
}
