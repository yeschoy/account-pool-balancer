package opencode

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestProtocolConversionsRetainLargeIntegerArguments(t *testing.T) {
	const number = "900719925474099312345"
	for _, from := range []Protocol{Chat, Responses, Messages} {
		body := bytes.ReplaceAll(toolHistory(from), []byte(`\"q\":1`), []byte(`\"q\":`+number))
		if from == Messages {
			body = bytes.ReplaceAll(body, []byte(`"q":1`), []byte(`"q":`+number))
		}
		for _, to := range []Protocol{Chat, Responses, Messages} {
			out, err := ConvertRequest(from, to, body)
			if err != nil {
				t.Fatal(from, to, err)
			}
			if !bytes.Contains(out, []byte(number)) {
				t.Fatalf("argument rounded %s -> %s: %s", from, to, out)
			}
		}
	}
	input := []byte(`{"id":"msg_1","model":"m","content":[{"type":"tool_use","id":"original","name":"lookup","input":{"q":` + number + `}}],"stop_reason":"tool_use","usage":{"input_tokens":7,"output_tokens":3}}`)
	reply, err := DecodeReply(Messages, input)
	if err != nil {
		t.Fatal(err)
	}
	for _, to := range []Protocol{Chat, Responses, Messages} {
		m, err := reply.Encode(to)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(m)
		if !bytes.Contains(b, []byte(number)) {
			t.Fatal("reply rounded", string(b))
		}
		sse, err := reply.Stream(to)
		if err != nil {
			t.Fatal(err)
		}
		again, err := ReadStream(to, strings.NewReader(string(sse)))
		if err != nil {
			t.Fatal(to, err)
		}
		found := false
		for _, part := range again.Parts {
			if part.kind == "call" && strings.Contains(part.args, number) {
				found = true
			}
		}
		if !found || again.Usage.Input != 7 || again.Usage.Output != 3 {
			t.Fatalf("stream lost number or measured usage: %+v", again)
		}
	}
}
