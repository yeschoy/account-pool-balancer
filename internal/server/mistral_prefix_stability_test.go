package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"account-pool-balancer/internal/config"
)

// Decode independently of the production wire scanner so the test cannot hide
// a renderer that changes object order or reserializes nested tool schemas.
func mistralOrderedFieldsForTest(t *testing.T, body string) ([]string, map[string]json.RawMessage) {
	t.Helper()
	d := json.NewDecoder(strings.NewReader(body))
	if token, err := d.Token(); err != nil || token != json.Delim('{') {
		t.Fatal("expected JSON object", err)
	}
	var order []string
	fields := map[string]json.RawMessage{}
	for d.More() {
		token, err := d.Token()
		if err != nil {
			t.Fatal(err)
		}
		name, ok := token.(string)
		if !ok {
			t.Fatal("expected object key")
		}
		order = append(order, name)
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			t.Fatal(err)
		}
		fields[name] = value
	}
	if _, err := d.Token(); err != nil {
		t.Fatal(err)
	}
	return order, fields
}

func TestMistralHTTPGrowingHistoryKeepsFullWirePrefix(t *testing.T) {
	e := newMistralEnv(t)
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.Routing.Strategy = "smart"
		c.Routing.MaxBodyMB = 16
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var tools strings.Builder
	tools.WriteString("[\n")
	for i := 48; i >= 0; i-- {
		if i != 48 {
			tools.WriteString(",\n")
		}
		description, _ := json.Marshal(strings.Repeat(fmt.Sprintf("tool-%02d schema <keep> ", i), 90))
		fmt.Fprintf(&tools, `{ "type":"function", "function":{ "name":"tool_%02d", "description":%s, "parameters":{ "type":"object", "properties":{ "z_last":{"type":"string"}, "a_first":{"type":"integer"} }, "required":["z_last","a_first"] } } }`, i, description)
	}
	tools.WriteString("\n]")
	const metadata = `{"z":9007199254740993,"a":{"last":2,"first":1}}`
	system, _ := json.Marshal(strings.Repeat("stable system <prefix> 中文 ", 3000))
	messages := []json.RawMessage{
		json.RawMessage(`{"role":"system","content":` + string(system) + `}`),
		json.RawMessage(`{"role":"developer","content":"stable developer instructions"}`),
		json.RawMessage(`{"role":"user","content":[{"type":"text","text":"first user turn"}]}`),
	}
	var previous []json.RawMessage
	var firstAccountKey string
	for turn := 0; turn < 8; turn++ {
		history, err := json.Marshal(messages)
		if err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf(`{ "tools":%s, "model":"deepseek-v4-1-flash-1017", "metadata":%s, "messages":%s, "stream":false, "thinking":{"type":"enabled"}, "reasoning_effort":"high", "seed":9007199254740993, "store":false, "tool_choice":"auto" }`, tools.String(), metadata, history)
		if len(body) <= 64<<10 {
			t.Fatal("fixture must exceed the production detail log cap")
		}
		status, _ := e.post(t, "/v1/chat/completions", body, map[string]string{"Session-Id": "cache-order-session", "Thread-Id": "cache-order-session"})
		if status != 200 || e.count() != turn+1 {
			t.Fatalf("turn %d: status=%d attempts=%d", turn, status, e.count())
		}
		call := e.call(turn)
		if turn == 0 {
			firstAccountKey = call.key
		}
		if call.key != firstAccountKey || call.header.Get("X-Affinity") != "cache-order-session" {
			t.Fatalf("turn %d changed account or upstream affinity", turn)
		}
		order, fields := mistralOrderedFieldsForTest(t, call.body)
		wantOrder := []string{"tools", "model", "metadata", "messages", "stream", "reasoning_effort", "tool_choice", "random_seed", "prompt_cache_key"}
		if !reflect.DeepEqual(order, wantOrder) {
			t.Fatalf("turn %d reordered root fields: %v", turn, order)
		}
		if !bytes.Equal(fields["tools"], []byte(tools.String())) || !bytes.Equal(fields["metadata"], []byte(metadata)) {
			t.Fatalf("turn %d reserialized/reordered tool array, schema properties, required array or metadata", turn)
		}
		if string(fields["random_seed"]) != "9007199254740993" || string(fields["prompt_cache_key"]) != `"cache-order-session"` {
			t.Fatal("numeric precision or stable cache key changed")
		}
		var sent []json.RawMessage
		if err := json.Unmarshal(fields["messages"], &sent); err != nil || len(sent) != len(messages) {
			t.Fatal("history omitted, duplicated or malformed", err)
		}
		for i, old := range previous {
			if !bytes.Equal(old, sent[i]) {
				t.Fatalf("turn %d changed already-sent message %d", turn, i)
			}
		}
		for i, original := range messages {
			var source, actual map[string]json.RawMessage
			if json.Unmarshal(original, &source) != nil || json.Unmarshal(sent[i], &actual) != nil {
				t.Fatal("invalid complete message")
			}
			wantRole := source["role"]
			if string(wantRole) == `"developer"` {
				wantRole = json.RawMessage(`"system"`)
			}
			if !bytes.Equal(actual["role"], wantRole) || !bytes.Equal(actual["tool_calls"], source["tool_calls"]) || !bytes.Equal(actual["tool_call_id"], source["tool_call_id"]) {
				t.Fatalf("turn %d changed message %d role/order or tool-call association", turn, i)
			}
			if string(source["role"]) == `"tool"` && !bytes.Equal(actual["content"], source["content"]) {
				t.Fatal("large tool result was truncated or rewritten during forwarding")
			}
		}
		if !bytes.Equal(sent[0], messages[0]) || string(sent[1]) != `{"role":"system","content":"stable developer instructions"}` || !bytes.Equal(sent[2], messages[2]) {
			t.Fatal("initial prompt changed beyond the required developer role conversion")
		}
		previous = sent
		thought, _ := json.Marshal(fmt.Sprintf("thought %d <keep> 中文", turn))
		alias := "reasoning_content"
		if turn%2 != 0 {
			alias = "reasoning"
		}
		resultRepeats := 1000
		if turn == 5 {
			resultRepeats = 600000 // Exercise a multi-MiB history, not just log-sized fixtures.
		}
		messages = append(messages,
			json.RawMessage(fmt.Sprintf(`{"role":"assistant","content":"answer-%d","%s":%s,"tool_calls":[{"id":"call-%d","type":"function","function":{"name":"tool_48","arguments":"{\"z_last\":\"keep\",\"a_first\":9007199254740993}"}}]}`, turn, alias, thought, turn)),
			json.RawMessage(fmt.Sprintf(`{"role":"tool","tool_call_id":"call-%d","content":%q}`, turn, strings.Repeat("tool-result-", resultRepeats))),
			json.RawMessage(fmt.Sprintf(`{"role":"user","content":[{"type":"text","text":"next-%d"},{"type":"image_url","image_url":{"url":"https://example.invalid/test.png","MimeType":""}}]}`, turn)),
		)
	}
}
