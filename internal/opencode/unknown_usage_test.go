package opencode

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMissingUsageIsNotFabricatedAsZero(t *testing.T) {
	reply, err := DecodeReply(Chat, []byte(`{"id":"r","model":"m","choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if reply.Usage.Known {
		t.Fatal("missing usage treated as measured zero")
	}
	doc, err := reply.Encode(Responses)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["usage"]; ok {
		t.Fatal("invented usage")
	}
	if _, err := reply.Encode(Messages); err == nil {
		t.Fatal("required Messages usage silently fabricated")
	}
	b, err := ConvertRequest(Messages, Chat, []byte(`{"model":"m","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var m object
	_ = json.Unmarshal(b, &m)
	if obj(m["stream_options"])["include_usage"] != true {
		t.Fatal("converted Chat stream did not request usage")
	}
}
func TestMalformedOptionalFieldsAreNotSilentlyDropped(t *testing.T) {
	for _, field := range []string{`"tools":{}`, `"stream_options":5`, `"stream":"yes"`} {
		b := `{"model":"m","max_tokens":100,"messages":[{"role":"user","content":"hi"}],` + field + `}`
		if _, err := ConvertRequest(Chat, Messages, []byte(b)); err == nil {
			t.Fatalf("malformed field dropped: %s", b)
		}
	}
	if _, err := ReadStream(Chat, strings.NewReader("data: {\"id\":\"r\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n")); err == nil {
		t.Fatal("missing finish_reason hidden")
	}
}
