package server

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"hash"
)

// Incremental capture owns only digests, enums and lengths. Event text is read
// directly from the relay's current event and is never kept across events.
// SSE frames are transport fragments; adjacent continuation fragments form a
// logical block, not thousands of pretend native blocks for individual tokens.
type mistralLayoutBuilder struct {
	answer, thought hash.Hash
	blocks          []mistralLayoutBlock
	nodes           int
	total           int64
	native          bool
	blocked         bool
	committed       bool
}

type mistralLayoutCapture struct {
	store   *mistralLayouts
	scope   string
	log     *mistralLayoutLog
	choices map[int]*mistralLayoutBuilder
}

func (s *mistralLayouts) capture(scope string, log *mistralLayoutLog) *mistralLayoutCapture {
	if s == nil || scope == "" {
		return nil
	}
	return &mistralLayoutCapture{store: s, scope: scope, log: log, choices: map[int]*mistralLayoutBuilder{}}
}

func (c *mistralLayoutCapture) observe(body []byte, stream bool) {
	if c == nil {
		return
	}
	if bytes.Equal(bytes.TrimSpace(body), []byte("[DONE]")) {
		for _, b := range c.choices {
			c.commit(b)
		}
		return
	}
	var root struct {
		Choices []struct {
			Index        int             `json:"index"`
			Message      json.RawMessage `json:"message"`
			Delta        json.RawMessage `json:"delta"`
			FinishReason *string         `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal(body, &root) != nil {
		return
	}
	for _, choice := range root.Choices {
		if choice.Index < 0 || choice.Index >= 4 {
			continue // bounded unsupported choice is unknown, never guessed
		}
		b := c.choices[choice.Index]
		if b == nil {
			b = &mistralLayoutBuilder{answer: hmac.New(sha256.New, c.store.key), thought: hmac.New(sha256.New, c.store.key)}
			c.choices[choice.Index] = b
		}
		raw := choice.Message
		if stream {
			raw = choice.Delta
		}
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) == nil {
			// This is exactly the existing adapter's prefix for a mixed native
			// array plus an existing legacy reasoning_content value.
			if text := mistralString(m["reasoning_content"]); text != "" {
				b.add(mistralLayoutBlock{Kind: "thinking", Parts: []int{len(text)}}, []string{text}, stream)
			}
			content := bytes.TrimSpace(m["content"])
			if len(content) > 0 && content[0] == '[' {
				b.native = true
				var chunks []map[string]json.RawMessage
				if json.Unmarshal(content, &chunks) != nil {
					b.blocked = true
				}
				for ci, chunk := range chunks {
					switch mistralString(chunk["type"]) {
					case "text":
						text, err := mistralTextChunk(chunk)
						if err != nil {
							b.blocked = true
						}
						b.add(mistralLayoutBlock{Kind: "text", Bytes: len(text)}, []string{text}, stream && ci == 0)
					case "thinking":
						parts, err := mistralThinkingTextParts(chunk)
						block := mistralLayoutBlock{Kind: "thinking", Closed: mistralLayoutEnum(chunk["closed"], "false", "true", "null"), Signature: mistralLayoutEnum(chunk["signature"], "null", `""`)}
						if err != nil || block.Closed < 0 || block.Signature < 0 {
							b.blocked = true
						}
						for _, text := range parts {
							block.Parts = append(block.Parts, len(text))
						}
						b.add(block, parts, stream && ci == 0)
					default:
						b.blocked = true // response adapter owns actual error/forwarding
					}
				}
			} else if len(content) > 0 && content[0] == '"' {
				text := mistralString(content)
				if text != "" {
					b.add(mistralLayoutBlock{Kind: "text", Bytes: len(text)}, []string{text}, stream)
				}
			}
		}
		// Commit before a client can read a finish/DONE and send its next
		// request. No full-stream buffering or delayed first-event flush.
		if !stream || choice.FinishReason != nil && *choice.FinishReason != "" {
			c.commit(b)
		}
	}
}

func mistralLayoutEnum(raw []byte, values ...string) int {
	if len(raw) == 0 {
		return 0
	}
	for i, value := range values {
		if bytes.Equal(bytes.TrimSpace(raw), []byte(value)) {
			return i + 1
		}
	}
	return -1
}

func (b *mistralLayoutBuilder) add(block mistralLayoutBlock, texts []string, continuation bool) {
	for _, text := range texts {
		if block.Kind == "text" {
			b.answer.Write([]byte(text))
		} else {
			b.thought.Write([]byte(text))
		}
		b.total += int64(len(text))
	}
	if b.total > 512<<20 {
		b.blocked = true
	}
	if b.blocked {
		b.blocks = nil
		return
	}
	if continuation && len(b.blocks) > 0 {
		last := &b.blocks[len(b.blocks)-1]
		if last.Kind == block.Kind {
			if block.Kind == "text" {
				last.Bytes += block.Bytes
			} else {
				for i, n := range block.Parts {
					if i == 0 && len(last.Parts) > 0 {
						last.Parts[len(last.Parts)-1] += n
					} else {
						last.Parts = append(last.Parts, n)
						b.nodes++
					}
				}
				if block.Closed != 0 {
					last.Closed = block.Closed
				}
				if block.Signature != 0 {
					last.Signature = block.Signature
				}
			}
			if b.nodes > mistralLayoutParts {
				b.blocked, b.blocks = true, nil
			}
			return
		}
	}
	b.blocks = append(b.blocks, block)
	b.nodes += 1 + len(block.Parts)
	if b.nodes > mistralLayoutParts {
		b.blocked, b.blocks = true, nil
	}
}

func (c *mistralLayoutCapture) commit(b *mistralLayoutBuilder) {
	if b.committed {
		return
	}
	b.committed = true
	key := c.store.tag("reply", c.scope, hex.EncodeToString(b.answer.Sum(nil)), hex.EncodeToString(b.thought.Sum(nil)))
	r := mistralLayoutRecord{Key: key, Blocks: b.blocks}
	switch {
	case b.blocked:
		r.State, r.Blocks = "unavailable", nil
		c.log.CaptureUnavailable++
	case !b.native:
		// Scalar-only upstream did not provide an original layout. Record
		// unknown so it cannot be mistaken for a different native response.
		r.State, r.Blocks = "unknown", nil
	default:
		c.log.Captured++
	}
	c.store.put(r)
}
