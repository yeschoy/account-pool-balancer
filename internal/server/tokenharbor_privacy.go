package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"account-pool-balancer/internal/config"
)

func thIdentityValues(h http.Header, body map[string]any, extra []string) []string {
	var out []string
	add := func(v string) {
		if v, ok := validValue(v); ok && len(v) >= 8 {
			out = append(out, v)
		}
	}
	for name, values := range h {
		if !isSecretHeader(name) {
			continue
		}
		for _, value := range values {
			add(value)
			if parts := strings.Fields(value); len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
				add(parts[1])
			}
			if strings.EqualFold(name, "cookie") {
				for _, part := range strings.Split(value, ";") {
					_, secret, ok := strings.Cut(part, "=")
					if ok {
						add(strings.TrimSpace(secret))
					}
				}
			}
		}
	}
	for _, x := range sessionHeaders {
		add(headerValue(h, x.name))
	}
	for _, x := range parentHeaders {
		add(headerValue(h, x))
	}
	for _, x := range extra {
		add(headerValue(h, x))
	}
	for _, x := range []string{"X-Prompt-Cache-Key", "Prompt-Cache-Key", "X-Cache-Key", "X-Client-Request-Id", "X-Affinity"} {
		add(headerValue(h, x))
	}
	for _, x := range bodyKeys {
		add(lookup(body, x.path))
	}
	for _, x := range []string{"previous_response_id", "metadata.user_id", "metadata.parent_session_id", "user", "promptCacheKey"} {
		add(lookup(body, x))
	}
	add(claudeUserSession(lookup(body, "metadata.user_id")))
	add(claudeUserSession(lookup(body, "user")))
	return out
}

func thPrivateHeaders(h http.Header) http.Header {
	out := goPrivateHeaders(h)
	for k := range out {
		if strings.EqualFold(k, "X-Client-Request-Id") || strings.EqualFold(k, "X-Cache-Key") || strings.EqualFold(k, "X-Affinity") {
			out[k] = []string{"[REDACTED]"}
		}
		for _, x := range sessionHeaders {
			if strings.EqualFold(k, x.name) {
				out[k] = []string{"[REDACTED]"}
			}
		}
	}
	return out
}

// Called only for bounded log capture, never for native upstream bytes.
func thPrivateBody(b []byte, identities []string) []byte {
	if len(b) == 0 {
		return nil
	}
	var redact func(any)
	redact = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, c := range x {
				l := strings.ToLower(k)
				if id, ok := c.(string); ok && l == "id" && strings.HasPrefix(id, "resp_") {
					x[k] = "[REDACTED]"
					continue
				}
				if thPrivateKey(k) {
					x[k] = "[REDACTED]"
				} else {
					redact(c)
				}
			}
		case []any:
			for _, c := range x {
				redact(c)
			}
		}
	}
	one := func(raw []byte) []byte {
		var v any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if !json.Valid(raw) || decoder.Decode(&v) != nil {
			return []byte("[body omitted: identity redaction unavailable]")
		}
		redact(v)
		out, _ := json.Marshal(v)
		return out
	}
	var out []byte
	if json.Valid(b) {
		out = one(b)
	} else if bytes.Contains(b, []byte("data:")) {
		var text bytes.Buffer
		for _, line := range bytes.Split(b, []byte("\n")) {
			if bytes.HasPrefix(line, []byte("data:")) {
				text.WriteString("data: ")
				payload := bytes.TrimSpace(line[5:])
				if bytes.Equal(payload, []byte("[DONE]")) {
					text.Write(payload)
				} else {
					text.Write(one(payload))
				}
				text.WriteByte('\n')
			} else if bytes.HasPrefix(line, []byte("event:")) {
				text.Write(line)
				text.WriteByte('\n')
			}
		}
		out = text.Bytes()
	} else {
		out = []byte("[body omitted: identity redaction unavailable]")
	}
	text := string(out)
	for _, id := range identities {
		text = strings.ReplaceAll(text, id, "[REDACTED]")
	}
	return []byte(text)
}

type discardLogBody struct{}

func (discardLogBody) Header() http.Header         { return nil }
func (discardLogBody) WriteHeader(int)             {}
func (discardLogBody) Write(b []byte) (int, error) { return len(b), nil }

func thPrivateKey(k string) bool {
	l := strings.ToLower(k)
	return isSecretHeader(k) || strings.Contains(l, "session") || strings.Contains(l, "conversation") || strings.Contains(l, "thread") || strings.Contains(l, "prompt_cache") || l == "promptcachekey" || strings.Contains(l, "affinity") || l == "user_id" || l == "previous_response_id" || l == "response_id"
}

// Scan full JSON without decoding/retaining message trees. Identity fields are
// removed BEFORE a bounded head/tail is selected, including metadata at the
// end of very large histories. Native forwarding bytes are never touched.
func thBodyPreview(body []byte, identities []string, limit int) ([]byte, bool) {
	if len(body) == 0 {
		return nil, false
	}
	if len(body) > 8<<20 {
		return []byte("[body omitted: exceeds 8 MiB diagnostic scan bound]"), true
	}
	if !json.Valid(body) {
		clipped, cut := clipMiddle(body, limit)
		return thPrivateBody(clipped, identities), cut
	}
	capture := &captureWriter{ResponseWriter: discardLogBody{}, max: limit}
	write := func(b []byte) { _, _ = capture.Write(b) }
	var walk func(int, int) int
	walk = func(i, depth int) int {
		i = skipSpace(body, i)
		end := skipValue(body, i)
		if depth > 128 {
			write([]byte(`"[nested body omitted]"`))
			return end
		}
		switch body[i] {
		case '{':
			write([]byte("{"))
			i = skipSpace(body, i+1)
			first := true
			for body[i] != '}' {
				if !first {
					write([]byte(","))
				}
				first = false
				ke := skipString(body, i)
				var key string
				_ = json.Unmarshal(body[i:ke], &key)
				write(body[i:ke])
				write([]byte(":"))
				vs := skipSpace(body, skipSpace(body, ke)+1)
				ve := skipValue(body, vs)
				private := thPrivateKey(key)
				if key == "id" && body[vs] == '"' {
					if ve-vs > 1024 {
						private = true
					} else {
						var id string
						_ = json.Unmarshal(body[vs:ve], &id)
						private = strings.HasPrefix(id, "resp_")
					}
				}
				if private {
					write([]byte(`"[REDACTED]"`))
					i = ve
				} else {
					i = walk(vs, depth+1)
				}
				i = skipSpace(body, i)
				if body[i] == ',' {
					i = skipSpace(body, i+1)
				}
			}
			write([]byte("}"))
			return i + 1
		case '[':
			write([]byte("["))
			i = skipSpace(body, i+1)
			first := true
			for body[i] != ']' {
				if !first {
					write([]byte(","))
				}
				first = false
				i = walk(i, depth+1)
				i = skipSpace(body, i)
				if body[i] == ',' {
					i = skipSpace(body, i+1)
				}
			}
			write([]byte("]"))
			return i + 1
		default:
			write(body[i:end])
			return end
		}
	}
	walk(0, 0)
	out, cut := capture.body()
	text := string(out)
	for _, id := range identities {
		text = strings.ReplaceAll(text, id, "[REDACTED]")
	}
	return []byte(text), cut
}

// Each failed attempt has its own bounded record, even if a later attempt
// succeeds. Stream errors call this before downstream completion; dedupe by
// request-local attempt index. No original request map/body is retained here.
func (s *Server) thFailureDetail(cfg *config.Config, rec *requestLog, r *http.Request, body []byte, res *attemptResult, index int) {
	if rec.Provider == config.ProviderTokenHarbor && !cfg.TokenHarbor.DetailLog || rec.Provider == config.ProviderDeploy && !cfg.Deploy.DetailLog {
		return
	}
	if rec.failureDetails[index] {
		return
	}
	if rec.failureDetails == nil {
		rec.failureDetails = map[int]bool{}
	}
	rec.failureDetails[index] = true
	limit := min(cfg.Logs.DetailBodyKB, 64) << 10
	req, reqCut := thBodyPreview(body, res.privateIdentities, limit)
	resp, respCut := thBodyPreview(res.body, res.privateIdentities, limit)
	requestText, _ := maskBody(cfg, req, limit)
	responseText, responseCut := maskBody(cfg, resp, limit)
	headers := maskHeaders(cfg, thPrivateHeaders(r.Header))
	for _, name := range cfg.Routing.SessionHeaders {
		for key := range headers {
			if strings.EqualFold(key, name) {
				headers[key] = []string{"[REDACTED]"}
			}
		}
	}
	status := res.status
	if status < 100 {
		status = 502
	}
	s.logs.Append("details", rec.TS, &detailLog{TS: rec.TS, RequestID: rec.RequestID, AttemptIndex: index, Context: "failed_attempt", Terminal: res.terminal, Protocol: rec.Protocol, Method: r.Method, Path: r.URL.Path, Model: rec.Model, ClientKeyID: rec.ClientKeyID, ClientKeyName: rec.ClientKeyName, Provider: rec.Provider, AccountID: rec.AccountID, Status: status, Result: "failed", RequestHeaders: headers, RequestBody: requestText, RequestBodyBytes: len(body), RequestBodyTruncated: reqCut, ResponseHeaders: maskHeaders(cfg, thPrivateHeaders(res.header)), ResponseBody: responseText, ResponseBodyBytes: len(res.body), ResponseBodyTruncated: respCut || responseCut})
}
