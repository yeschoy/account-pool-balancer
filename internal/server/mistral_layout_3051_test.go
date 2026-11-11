package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Production3051: closed=true is emitted on many stream fragments. A request
// may contain at most one leading ThinkChunk, not one per response fragment.
func TestMistralLayout3051ClosedFragments(t *testing.T) {
	e := newMistralEnv(t)
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		if e.count() == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			for i := 0; i < 600; i++ {
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":[{\"type\":\"thinking\",\"thinking\":[{\"type\":\"text\",\"text\":\"r\"}],\"closed\":true}]}}]}\n\n")
			}
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":[{\"type\":\"text\",\"text\":\"answer\"}]},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		var root struct {
			Messages []struct {
				Content []struct {
					Type     string
					Thinking []struct{ Text string }
				}
			}
		}
		_ = json.Unmarshal([]byte(e.call(e.count()-1).body), &root)
		leading := 0
		if len(root.Messages) > 1 {
			for _, c := range root.Messages[1].Content {
				if c.Type != "thinking" {
					break
				}
				leading++
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if leading != 1 {
			w.WriteHeader(400)
			fmt.Fprintf(w, `{"message":"Expected at most one leading ThinkChunk, got %d","code":3051}`, leading)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	})
	if st, _ := e.post(t, "/v1/chat/completions", `{"model":"deepseek-v4-1-flash-1017","stream":true,"messages":[{"role":"user","content":"local question"}]}`, map[string]string{"Session-Id": "local-3051"}); st != 200 {
		t.Fatal("first stream failed")
	}
	body := fmt.Sprintf(`{"model":"deepseek-v4-1-flash-1017","messages":[{"role":"user","content":"local question"},{"role":"assistant","reasoning_content":%q,"content":"answer"},{"role":"user","content":"next"}]}`, strings.Repeat("r", 600))
	if st, reply := e.post(t, "/v1/chat/completions", body, map[string]string{"Session-Id": "local-3051"}); st != 200 || e.count() != 2 {
		t.Fatalf("closed fragments caused3051/local block: %d %s attempts=%d", st, reply, e.count())
	}
	r := e.s.mistralLayouts.export()
	found := false
	for _, rec := range r {
		if rec.State == "" && len(rec.Blocks) == 2 && rec.Blocks[0].Kind == "thinking" && len(rec.Blocks[0].Parts) == 1 && rec.Blocks[0].Parts[0] == 600 {
			found = true
		}
	}
	if !found {
		t.Fatal("closed per-token deltas exceeded budget or stayed separate")
	}
}

func TestMistralLayout3051ExistingCheckpoint(t *testing.T) {
	store := newMistralLayouts([]byte("local-secret"))
	key := store.textKey("local-scope", "answer", strings.Repeat("r", 168))
	blocks := make([]mistralLayoutBlock, 0, 169)
	for i := 0; i < 168; i++ {
		blocks = append(blocks, mistralLayoutBlock{Kind: "thinking", Parts: []int{1}, Closed: 2})
	}
	blocks = append(blocks, mistralLayoutBlock{Kind: "text", Bytes: 6})
	store.put(mistralLayoutRecord{Key: key, Blocks: blocks})
	r, ok, err := store.get(key)
	if err != nil || !ok {
		t.Fatal("old checkpoint missing")
	}
	raw, err := r.render("answer", strings.Repeat("r", 168))
	if err != nil {
		t.Fatal(err)
	}
	var chunks []struct {
		Type     string
		Thinking []struct{ Text string }
	}
	_ = json.Unmarshal(raw, &chunks)
	if len(chunks) != 2 || chunks[0].Type != "thinking" || len(chunks[0].Thinking) != 1 || chunks[0].Thinking[0].Text != strings.Repeat("r", 168) {
		t.Fatalf("existing fragmented checkpoint still replays many ThinkChunks: %s", raw)
	}
	// Corrected future capture is semantically identical, not an ambiguity.
	store.put(mistralLayoutRecord{Key: key, Blocks: []mistralLayoutBlock{{Kind: "thinking", Parts: []int{168}, Closed: 2}, {Kind: "text", Bytes: 6}}})
	if _, ok, err := store.get(key); err != nil || !ok {
		t.Fatal("corrected equivalent capture poisoned old checkpoint")
	}
}
