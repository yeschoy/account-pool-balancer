package opencode

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
)

const (
	maxGeminiReplyBytes = 16 << 20
	maxGeminiEventBytes = 2 << 20
)

type geminiReply struct {
	reply      Reply
	text       strings.Builder
	usage      object
	calls      map[string]bool
	finished   bool
	signedText bool
}

// ReadGeminiReply buffers a single bounded GenerateContentResponse or Gemini
// SSE stream. It never reports success without a real candidate finish reason.
func ReadGeminiReply(r io.Reader, stream bool) (*Reply, error) {
	g := geminiReply{usage: object{}, calls: map[string]bool{}}
	limit := &io.LimitedReader{R: r, N: maxGeminiReplyBytes + 1}
	if stream {
		if err := g.readStream(limit); err != nil {
			return nil, err
		}
	} else {
		body, err := io.ReadAll(limit)
		if err != nil {
			return nil, err
		}
		if limit.N == 0 {
			return nil, errors.New("Gemini compatibility response exceeds 16 MiB")
		}
		if err := g.consume(body); err != nil {
			return nil, err
		}
	}
	if limit.N == 0 {
		return nil, errors.New("Gemini compatibility response exceeds 16 MiB")
	}
	if !g.finished {
		return nil, errors.New("Gemini compatibility response missing finishReason")
	}
	g.flushText()
	if len(g.reply.Parts) == 0 && g.reply.Stop == "stop" {
		return nil, errors.New("Gemini response completed without text or a function call")
	}
	if len(g.calls) > 0 {
		if g.signedText {
			return nil, unsupported("Gemini tool turn with native replay signatures")
		}
		if g.reply.Stop == "stop" {
			g.reply.Stop = "tools"
		}
	}
	usage, err := geminiTokens(g.usage)
	if err != nil {
		return nil, err
	}
	g.reply.Usage = usage
	g.reply.gemini = true
	return &g.reply, nil
}

func (g *geminiReply) readStream(r io.Reader) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), maxGeminiEventBytes+1)
	var data bytes.Buffer
	eventBytes := 0
	isError, done := false, false
	for scanner.Scan() {
		line := scanner.Bytes()
		eventBytes += len(line) + 1
		if eventBytes > maxGeminiEventBytes {
			return errors.New("Gemini compatibility stream event exceeds 2 MiB")
		}
		switch {
		case len(line) == 0:
			if data.Len() > 0 {
				if done {
					return errors.New("Gemini stream sent data after DONE")
				}
				payload := bytes.TrimSpace(data.Bytes())
				switch {
				case isError:
					return g.streamError(payload)
				case bytes.Equal(payload, []byte("[DONE]")):
					done = true // Not a substitute for Gemini's finishReason.
				default:
					if err := g.consume(payload); err != nil {
						return err
					}
				}
			}
			data.Reset()
			eventBytes, isError = 0, false
		case bytes.HasPrefix(line, []byte("data:")):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.Write(bytes.TrimPrefix(line[len("data:"):], []byte(" ")))
		case bytes.HasPrefix(line, []byte("event:")):
			isError = bytes.Equal(bytes.TrimSpace(line[len("event:"):]), []byte("error"))
		}
	}
	if err := scanner.Err(); err != nil {
		return errors.New("Gemini compatibility stream read failed or event exceeds 2 MiB")
	}
	if data.Len() > 0 || isError {
		return errors.New("Gemini stream ended in an incomplete event")
	}
	return nil
}

func (g *geminiReply) streamError(payload []byte) error {
	if !json.Valid(payload) {
		return errors.New("invalid Gemini error payload")
	}
	return &StreamError{Payload: bytes.Clone(payload), BeforeContent: !g.finished && g.text.Len() == 0 && len(g.reply.Parts) == 0}
}

func (g *geminiReply) consume(payload []byte) error {
	var m object
	if decodeJSON(payload, &m) != nil || m == nil {
		return errors.New("invalid Gemini response JSON")
	}
	if m["error"] != nil || m["type"] == "error" {
		return g.streamError(payload)
	}
	if feedback := obj(m["promptFeedback"]); str(feedback["blockReason"]) != "" && feedback["blockReason"] != "BLOCK_REASON_UNSPECIFIED" {
		return unsupported("Gemini blocked the prompt")
	}
	if id := str(m["responseId"]); id != "" {
		if g.reply.ID != "" && g.reply.ID != id {
			return errors.New("Gemini stream changed response ID")
		}
		g.reply.ID = id
	}
	if raw := m["usageMetadata"]; raw != nil {
		u := obj(raw)
		if u == nil {
			return errors.New("invalid Gemini usageMetadata")
		}
		for _, k := range []string{"promptTokenCount", "candidatesTokenCount", "cachedContentTokenCount", "thoughtsTokenCount", "totalTokenCount", "toolUsePromptTokenCount"} {
			if v := u[k]; v != nil {
				g.usage[k] = v
			}
		}
	}
	if m["candidates"] == nil {
		return nil // A usage-only frame may follow finishReason.
	}
	cs := arr(m["candidates"])
	if cs == nil || len(cs) > 1 {
		return unsupported("multiple or invalid Gemini candidates")
	}
	if len(cs) == 0 {
		return nil
	}
	c := obj(cs[0])
	if c == nil || g.finished || c["index"] != nil && c["index"] != json.Number("0") {
		return errors.New("invalid or unexpected Gemini candidate")
	}
	if c["citationMetadata"] != nil || c["groundingMetadata"] != nil || c["urlContextMetadata"] != nil {
		return unsupported("Gemini citations or grounding metadata")
	}
	if raw := c["content"]; raw != nil {
		content := obj(raw)
		if content == nil || content["role"] != nil && content["role"] != "model" || content["parts"] != nil && arr(content["parts"]) == nil {
			return errors.New("invalid Gemini candidate content")
		}
		for _, item := range arr(content["parts"]) {
			if err := g.addPart(obj(item)); err != nil {
				return err
			}
		}
	}
	switch c["finishReason"] {
	case nil, "", "FINISH_REASON_UNSPECIFIED":
	case "STOP":
		g.finished, g.reply.Stop = true, "stop"
	case "MAX_TOKENS":
		g.finished, g.reply.Stop = true, "length"
	default:
		return unsupported("Gemini finishReason (blocked or unsupported output)")
	}
	return nil
}

func (g *geminiReply) addPart(p object) error {
	if p == nil || p["thought"] != nil && p["thought"] != false {
		return unsupported("Gemini thought content")
	}
	if p["text"] != nil {
		if err := fields(p, "text thought thoughtSignature"); err != nil {
			return err
		}
		text, ok := p["text"].(string)
		if !ok {
			return unsupported("non-string Gemini text")
		}
		// A visible text-only turn can be replayed without its optional
		// signature. Never drop a signature from a turn that calls tools.
		if p["thoughtSignature"] != nil {
			sig, ok := p["thoughtSignature"].(string)
			if !ok {
				return unsupported("non-string Gemini thoughtSignature")
			}
			g.signedText = g.signedText || sig != ""
		}
		g.text.WriteString(text)
		return nil
	}
	f := obj(p["functionCall"])
	if f == nil || p["thoughtSignature"] != nil {
		return unsupported("non-text Gemini output or signed function call")
	}
	if err := fields(p, "functionCall thought"); err != nil {
		return err
	}
	if err := fields(f, "id name args"); err != nil {
		return err
	}
	id, name := str(f["id"]), str(f["name"])
	args := f["args"]
	if args == nil {
		args = object{}
	}
	if id == "" {
		id = "call_" + rand.Text()
	}
	if name == "" || g.calls[id] || obj(args) == nil {
		return errors.New("invalid Gemini function call")
	}
	b, err := json.Marshal(args)
	if err != nil {
		return err
	}
	g.calls[id] = true
	g.flushText()
	g.reply.Parts = append(g.reply.Parts, part{kind: "call", id: id, name: name, args: string(b)})
	return nil
}

func (g *geminiReply) flushText() {
	if g.text.Len() > 0 {
		g.reply.Parts = append(g.reply.Parts, part{kind: "text", text: g.text.String()})
		g.text.Reset()
	}
}

func geminiInt(v any) (int64, bool) {
	n, ok := v.(json.Number)
	i, err := n.Int64()
	return i, ok && err == nil && i >= 0
}

func geminiTokens(m object) (Tokens, error) {
	for _, v := range m {
		if _, ok := geminiInt(v); !ok {
			return Tokens{}, errors.New("invalid Gemini token count")
		}
	}
	in, hasIn := geminiInt(m["promptTokenCount"])
	visible, hasOut := geminiInt(m["candidatesTokenCount"])
	thoughts, hasThoughts := geminiInt(m["thoughtsTokenCount"])
	cached, _ := geminiInt(m["cachedContentTokenCount"])
	tool, _ := geminiInt(m["toolUsePromptTokenCount"])
	if hasIn && !hasOut && tool == 0 {
		// Zero-valued proto fields can be omitted. Infer visible output only
		// from a measured total and its known components.
		if total, ok := geminiInt(m["totalTokenCount"]); ok && total >= in && total-in >= thoughts {
			visible, hasOut = total-in-thoughts, true
		}
	}
	if cached > in && hasIn || visible > math.MaxInt64-thoughts || in > math.MaxInt64-visible-thoughts {
		return Tokens{}, errors.New("inconsistent Gemini token counts")
	}
	return Tokens{Known: hasIn && hasOut, Input: in, Output: visible + thoughts, CachedKnown: hasIn, Cached: cached, ReasoningKnown: hasThoughts, Reasoning: thoughts}, nil
}
