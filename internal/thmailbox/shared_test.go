package thmailbox

import (
	"testing"
	"time"
)

func TestSharedMailboxVerificationMessage(t *testing.T) {
	if err := Validate("https://191006.xyz/mailbox/mock-token-for-tests"); err != nil {
		t.Fatal(err)
	}
	if err := Validate("http://191006.xyz/mailbox/mock-token-for-tests"); err == nil {
		t.Fatal("insecure share URL accepted")
	}
	page := `<div class="subject">Verify Token Harbor email</div><div class="label">发件人</div><div class="value">Token Harbor</div><div class="label">时间</div><div class="value">2026-11-06T06:03:37Z</div><section><div class="content"><a href="https://tokenharbor.ai/verify-email?token=mock-token">Verify email</a></div></section>`
	messages := parseSharedMessages([]byte(page))
	if len(messages) != 1 || messages[0].ReceivedAt != time.Date(2026, 11, 6, 6, 3, 37, 0, time.UTC) || messages[0].From != "Token Harbor" {
		t.Fatal("shared mailbox fields not parsed")
	}
	if messages[0].Text != "Verify email\nhttps://tokenharbor.ai/verify-email?token=mock-token" {
		t.Fatalf("verification href lost: %q", messages[0].Text)
	}
	if len(parseSharedMessages([]byte("empty mailbox"))) != 0 {
		t.Fatal("invented a message")
	}
}
