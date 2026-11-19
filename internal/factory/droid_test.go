package factory

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestOpenDroidBodyResponsesAndChat(t *testing.T) {
	for _, c := range []struct{ protocol, body, want string }{
		// Responses: instructions are joined with droid's line.
		{"responses", `{"model":"gpt-5.4","instructions":"Original","input":"hi"}`,
			`{"input":"hi","instructions":"` + DroidLine + `\nOriginal","model":"gpt-5.4"}`},
		// No instructions at all: the line alone.
		{"responses", `{"model":"gpt-5.4","input":"hi"}`,
			`{"input":"hi","instructions":"` + DroidLine + `","model":"gpt-5.4"}`},
		// Empty instructions collapse to the line, not to a blank line.
		{"responses", `{"model":"gpt-5.4","instructions":"   ","input":"hi"}`,
			`{"input":"hi","instructions":"` + DroidLine + `","model":"gpt-5.4"}`},
		// A non-string instructions value is not something droid sends.
		{"responses", `{"model":"gpt-5.4","instructions":[{"type":"text","text":"x"}],"input":"hi"}`,
			`{"model":"gpt-5.4","instructions":[{"type":"text","text":"x"}],"input":"hi"}`},
		// Chat: the first system message opens with the line.
		{"chat", `{"model":"m","messages":[{"role":"system","content":"Original"},{"role":"user","content":"hi"}]}`,
			`{"messages":[{"content":"` + DroidLine + `\nOriginal","role":"system"},{"content":"hi","role":"user"}],"model":"m"}`},
		// Chat with no system message: one is prepended.
		{"chat", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
			`{"messages":[{"content":"` + DroidLine + `","role":"system"},{"content":"hi","role":"user"}],"model":"m"}`},
		// Text-only part arrays are joined into the string droid sends.
		{"chat", `{"model":"m","messages":[{"role":"system","content":[{"type":"text","text":"A"},{"type":"text","text":"B"}]}]}`,
			`{"messages":[{"content":"` + DroidLine + `\nA\nB","role":"system"}],"model":"m"}`},
		// A non-text part keeps the array, with the line added as a block.
		{"chat", `{"model":"m","messages":[{"role":"system","content":[{"type":"image_url","image_url":{"url":"x"}}]}]}`,
			`{"messages":[{"content":[{"text":"` + DroidLine + `","type":"text"},{"image_url":{"url":"x"},"type":"image_url"}],"role":"system"}],"model":"m"}`},
		{"chat", `not json`, `not json`},
	} {
		got := string(OpenDroidBody(c.protocol, []byte(c.body)))
		if got != c.want {
			t.Fatalf("%s\n got %s\nwant %s", c.protocol, got, c.want)
		}
	}
}

func TestOpenDroidBodyIdempotentAndNumeric(t *testing.T) {
	// An already-opened body is returned byte for byte, whatever its formatting.
	for _, c := range []struct{ protocol, body string }{
		{"responses", `{ "model" : "gpt-5.4", "instructions" : "` + DroidLine + `\nOriginal", "input":"hi" }`},
		{"chat", `{"messages":[{"role":"system","content":"` + DroidLine + `"}]}`},
		{"messages", `{"system":[{"type":"text","text":"` + DroidLine + `"}]}`},
		{"responses", `{"model":"gpt-5.4","input":"hi","x":`},
		{"chat", `[]`},
	} {
		if got := string(OpenDroidBody(c.protocol, []byte(c.body))); got != c.body {
			t.Fatalf("%s: unopened body rewritten\n got %s\nwant %s", c.protocol, got, c.body)
		}
	}
	// A number beyond 2^53 survives the round trip because the decoder keeps
	// json.Number; a plain float64 decode would round it.
	out := OpenDroidBody("chat", []byte(`{"model":"m","seed":9007199254740993,"messages":[]}`))
	if !strings.Contains(string(out), "9007199254740993") {
		t.Fatal("large integer rounded", string(out))
	}
	var doc map[string]any
	dec := json.NewDecoder(strings.NewReader(string(out)))
	dec.UseNumber()
	if dec.Decode(&doc) != nil || doc["seed"].(json.Number).String() != "9007199254740993" {
		t.Fatal("large integer not exact", string(out))
	}
	// HTML-looking text is not escaped into \u003c forms that droid never sends.
	esc := string(OpenDroidBody("responses", []byte(`{"input":"a<b>&c"}`)))
	if !strings.Contains(esc, `a<b>&c`) {
		t.Fatal("HTML escaped", esc)
	}
}

func TestOpenDroidBodyAnthropic(t *testing.T) {
	// Claude Code's billing header block is consumed and droid's line is first.
	body := `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.0;"},{"type":"text","text":"You are Claude Code."},{"type":"text","text":"Task"}],"messages":[{"role":"user","content":"hi"}]}`
	type block struct{ Type, Text string }
	var doc struct {
		System []block `json:"system"`
	}
	if err := json.Unmarshal(OpenDroidBody("messages", []byte(body)), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.System) != 3 || doc.System[0].Text != DroidLine || doc.System[1].Text != "You are Claude Code." || doc.System[2].Text != "Task" {
		t.Fatalf("unexpected system: %+v", doc.System)
	}
	// A billing block that also carries instructions keeps them.
	keep := `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.0;\nAuto mode: classify"}],"messages":[]}`
	if err := json.Unmarshal(OpenDroidBody("messages", []byte(keep)), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.System) != 2 || doc.System[0].Text != DroidLine || doc.System[1].Text != "Auto mode: classify" {
		t.Fatalf("instructions after the billing line lost: %+v", doc.System)
	}
	// A string system keeps its text after the billing line and the identity.
	s := `{"system":"x-anthropic-billing-header: cc_version=2.1.0;\nTask","messages":[]}`
	var str struct {
		System string `json:"system"`
	}
	if err := json.Unmarshal(OpenDroidBody("messages", []byte(s)), &str); err != nil {
		t.Fatalf("%v: %s", err, OpenDroidBody("messages", []byte(s)))
	}
	if str.System != DroidLine+"\nTask" {
		t.Fatalf("string system: %q", str.System)
	}
	// An identity that appears later is moved first and not duplicated.
	mixed := `{"system":[{"type":"text","text":"A"},{"type":"text","text":"` + DroidLine + `"},{"type":"text","text":"B"}],"messages":[]}`
	if err := json.Unmarshal(OpenDroidBody("messages", []byte(mixed)), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.System) != 3 || doc.System[0].Text != DroidLine || doc.System[1].Text != "A" || doc.System[2].Text != "B" {
		t.Fatalf("identity not placed first: %+v", doc.System)
	}
	// A system-less Messages request is opened with the line alone.
	// A request with no system at all gets the line as a plain string, the
	// shape anthropicBody writes.
	empty := `{"messages":[{"role":"user","content":"hi"}]}`
	var fresh struct {
		System string `json:"system"`
	}
	if err := json.Unmarshal(OpenDroidBody("messages", []byte(empty)), &fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.System != DroidLine {
		t.Fatalf("missing identity: %q", fresh.System)
	}
}

func TestDroidClientHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("X-Factory-Client", "other")
	h.Set("User-Agent", "account-pool-balancer/factory-gateway")
	ApplyClientHeaders(h)
	if h.Get("User-Agent") != "factory-cli/"+DroidVersion || h.Get("X-Factory-Client") != "cli" || h.Get("X-Client-Version") != DroidVersion {
		t.Fatal("client identity", h)
	}
	tp := Traceparent("0123456789abcdef0123456789abcdef", "0123456789abcdef")
	ApplyForwardHeaders(h, "openai", "sess", "msg", SurfaceTUI, tp)
	if h.Get("X-Api-Provider") != "openai" || h.Get("X-Session-Id") != "sess" || h.Get("X-Assistant-Message-Id") != "msg" || h.Get("X-Provider-Routing-Source") != RoutingSourceConfiguredOrder || h.Get("X-Factory-Request-Surface") != SurfaceTUI || h.Get("Traceparent") != tp || h.Get("OpenAI-Platform") != "" || h.Get("X-Factory-Exec-Output-Format") != "" {
		t.Fatal("forward headers", h)
	}
	// exec also declares its output format; the surface is explicit.
	g := http.Header{}
	ApplyForwardHeaders(g, "anthropic", "", "", SurfaceExec, "")
	if g.Get("X-Factory-Request-Surface") != SurfaceExec || g.Get("X-Factory-Exec-Output-Format") != "json" || g.Get("X-Session-Id") != "" || g.Get("Traceparent") != "" {
		t.Fatal("exec headers", g)
	}
	ApplyAPIEndpointHeaders(g)
	if g.Get("X-Api-Key") != "placeholder" {
		t.Fatal("anthropic route key", g)
	}
	// The management plane is the Bun runtime, not the model client.
	m := http.Header{}
	ApplyManagementHeaders(m, SurfaceTUI)
	if m.Get("User-Agent") != "Bun/"+BunVersion || m.Get("X-Factory-Client") != "cli" || m.Get("X-Client-Version") != DroidVersion || m.Get("X-Factory-Request-Surface") != SurfaceTUI || m.Get("X-Factory-Exec-Output-Format") != "" {
		t.Fatal("management headers", m)
	}
	ApplyManagementHeaders(m, SurfaceExec)
	if m.Get("X-Factory-Exec-Output-Format") != "json" {
		t.Fatal("exec management headers", m)
	}
	// whoami sends neither the client pair nor the surface.
	w := http.Header{}
	ApplyWhoamiHeaders(w)
	if w.Get("User-Agent") != "Bun/"+BunVersion || w.Get("X-Factory-Whoami-Extended") != "true" || w.Get("X-Factory-Client") != "" || w.Get("X-Client-Version") != "" || w.Get("X-Factory-Request-Surface") != "" {
		t.Fatal("whoami headers", w)
	}
}

func TestTraceparentShape(t *testing.T) {
	for _, c := range []struct{ trace, span, want string }{
		{"0123456789abcdef0123456789abcdef", "0123456789abcdef", "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"},
		{"00000000000000000000000000000000", "0123456789abcdef", ""},
		{"0123456789abcdef0123456789abcdef", "0000000000000000", ""},
		{"z123456789abcdef0123456789abcdef", "0123456789abcdef", ""},
		{"short", "0123456789abcdef", ""},
	} {
		if got := Traceparent(c.trace, c.span); got != c.want {
			t.Fatalf("Traceparent(%q,%q)=%q want %q", c.trace, c.span, got, c.want)
		}
	}
	for i := 0; i < 8; i++ {
		tp := RandomTraceparent()
		if len(tp) != 55 || tp[:3] != "00-" || tp[35] != '-' || tp[52:] != "-01" {
			t.Fatal("random traceparent", tp)
		}
	}
}
