package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/quota"
)

// mistralWireObject is a view over already validated JSON. Replacement values
// are raw JSON; nil removes a field. Never decode unrelated numbers or collapse
// duplicate keys through a map. Each retained field owns its preceding comma,
// so consecutive removals cannot overlap (including at the end of an object).
type mistralWireField struct {
	key            string
	start, vs, end int
	value          []byte
	changed        bool
}

type mistralWireObject struct {
	body      []byte
	fields    []mistralWireField
	additions [][]byte
	end       int
}

func scanMistralObject(body []byte) *mistralWireObject {
	open := skipSpace(body, 0)
	if open >= len(body) || body[open] != '{' {
		return nil
	}
	o := &mistralWireObject{body: body}
	i := skipSpace(body, open+1)
	for body[i] != '}' {
		ke := skipString(body, i)
		var key string
		_ = json.Unmarshal(body[i:ke], &key)
		vs := skipSpace(body, skipSpace(body, ke)+1)
		ve := skipValue(body, vs)
		o.fields = append(o.fields, mistralWireField{key: key, start: i, vs: vs, end: ve, value: body[vs:ve]})
		i = skipSpace(body, ve)
		if body[i] == ',' {
			i = skipSpace(body, i+1)
		}
	}
	o.end = i
	return o
}

// field returns the last occurrence of key with its byte offsets, matching get().
func (o *mistralWireObject) field(key string) (mistralWireField, bool) {
	for i := len(o.fields) - 1; i >= 0; i-- {
		if o.fields[i].key == key {
			return o.fields[i], true
		}
	}
	return mistralWireField{}, false
}

// countMistralField reports how many occurrences of key the object carries.
func countMistralField(o *mistralWireObject, key string) int {
	n := 0
	for _, f := range o.fields {
		if f.key == key {
			n++
		}
	}
	return n
}

func (o *mistralWireObject) get(key string) []byte {
	for i := len(o.fields) - 1; i >= 0; i-- {
		if o.fields[i].key == key {
			return o.fields[i].value
		}
	}
	return nil
}

func (o *mistralWireObject) unique(keys ...string) error {
	for _, key := range keys {
		seen := false
		for _, f := range o.fields {
			if f.key == key {
				if seen {
					return fmt.Errorf("Mistral Chat duplicate %s is ambiguous; provide it once", key)
				}
				seen = true
			}
		}
	}
	return nil
}

func (o *mistralWireObject) set(key string, value []byte) {
	found := false
	for i := range o.fields {
		if o.fields[i].key == key {
			o.fields[i].value, o.fields[i].changed = value, true
			found = true
		}
	}
	if !found && value != nil {
		name, _ := json.Marshal(key)
		o.additions = append(o.additions, append(append(name, ':'), value...))
	}
}

func (o *mistralWireObject) changed() bool {
	if len(o.additions) != 0 {
		return true
	}
	for _, f := range o.fields {
		if f.changed {
			return true
		}
	}
	return false
}

func (o *mistralWireObject) render() []byte {
	if !o.changed() {
		return o.body
	}
	var out bytes.Buffer
	// Reserve headroom so growing a spliced body does not copy it twice.
	out.Grow(len(o.body) + len(o.body)/8 + 256)
	o.writeSplice(&out)
	return out.Bytes()
}

func (o *mistralWireObject) writeSplice(out *bytes.Buffer) {
	prefix, tail := o.end, o.end
	if len(o.fields) > 0 {
		prefix, tail = o.fields[0].start, o.fields[len(o.fields)-1].end
	}
	out.Write(o.body[:prefix])
	kept := false
	for i, f := range o.fields {
		if f.changed && f.value == nil {
			continue
		}
		if kept {
			// The separator before this field contains no other field bytes.
			out.Write(o.body[o.fields[i-1].end:f.start])
		}
		out.Write(o.body[f.start:f.vs])
		out.Write(f.value)
		kept = true
	}
	for _, field := range o.additions {
		if kept {
			out.WriteByte(',')
		}
		out.Write(field)
		kept = true
	}
	out.Write(o.body[tail:])
}

// Preserve the deployed cache-shard affinity behavior when updating master.
// A supplied X-Affinity wins; otherwise reuse the final private cache key.
func ensureMistralXAffinity(h http.Header, body []byte) {
	if h.Get("X-Affinity") != "" {
		return
	}
	if key := mistralFinalPromptCacheKey(body); key != "" {
		h.Set("X-Affinity", key)
	}
}

func mistralFinalPromptCacheKey(body []byte) string {
	o := scanMistralObject(body)
	if o == nil {
		return ""
	}
	raw := o.get("prompt_cache_key")
	if raw == nil || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

// mistralRequestCacheKey keeps explicit downstream cache/session headers intact
// on the wire. Account affinity still uses the caller-scoped identity HMAC.
func mistralRequestCacheKey(cfg *config.Config, h http.Header, ident Identity) (string, string) {
	for _, name := range []string{"X-Prompt-Cache-Key", "Prompt-Cache-Key"} {
		if value, ok := validValue(headerValue(h, name)); ok {
			return value, "cache_header"
		}
	}
	for _, names := range [][]string{cfg.Routing.SessionHeaders, parentHeaders} {
		for _, name := range names {
			if value, ok := validValue(headerValue(h, name)); ok {
				return value, "session_header"
			}
		}
	}
	for _, sh := range sessionHeaders {
		if value, ok := validValue(headerValue(h, sh.name)); ok {
			return value, "session_header"
		}
	}
	if ident.Fingerprint != "" {
		return "apb-mistral-" + ident.Fingerprint, "session_hmac"
	}
	return "", ""
}

// prepareMistralRequest runs after strict JSON/model validation and original
// identity selection, but before account/RPM acquisition. Three exact models
// share reasoning controls; unrelated DeepSeek parameters stay Chat-only.
func prepareMistralRequest(body []byte, path, cacheKey string) ([]byte, string, error) {
	if path != "/v1/chat/completions" && path != "/v1/responses" {
		return body, "", nil
	}
	o := scanMistralObject(body)
	model := mistralString(o.get("model"))
	reasoningModel := mistralReasoningModel(model)
	if path == "/v1/chat/completions" && reasoningModel {
		if err := adaptMistralChatStore(o); err != nil {
			return nil, "", err
		}
	}
	deepSeek := path == "/v1/chat/completions" && model == "deepseek-v4-1-flash-1017"
	if deepSeek {
		if err := adaptMistralDeepSeekParameters(o); err != nil {
			return nil, "", err
		}
	}
	if reasoningModel {
		if err := adaptMistralReasoningParameters(o, path == "/v1/responses"); err != nil {
			return nil, "", err
		}
	}
	if path == "/v1/chat/completions" {
		for i := range o.fields {
			if o.fields[i].key != "messages" {
				continue
			}
			messages, changed, err := mistralChatMessages(o.fields[i].value, deepSeek, reasoningModel)
			if err != nil {
				return nil, "", err
			}
			if changed {
				o.fields[i].value, o.fields[i].changed = messages, true
			}
		}
	}
	generated := ""
	if o.get("prompt_cache_key") == nil && cacheKey != "" {
		generated = cacheKey
		key, _ := json.Marshal(generated)
		o.set("prompt_cache_key", key)
	}
	return o.render(), generated, nil
}

// planKeyTemplate is the validated Chat metadata template the pool may inject,
// or "off" when the pool has no plan-key mode. It never invents a template: an
// unconfigured pool keeps the caller's bytes exactly.
func planKeyTemplate(cfg *config.Config) config.MistralChatMetadata {
	if v, ok := cfg.Mistral.EffectiveChatMetadata(); ok {
		return v
	}
	return config.MistralChatMetadata{Mode: config.MistralChatMetadataOff}
}

// applyMistralPlanKey appends the plan-key client markers to one upstream body.
// Measured on 2026-11-27 with the three kevin-11.26 Vibe keys: a Chat request
// without metadata.call_source is always answered 402 billing_api_budget_exhausted
// (code 2303) — the workspace's $30 api_budget is spent while $300 vibe_budget
// is untouched — and the same request with call_source returns 200 and debits
// vibe_budget. The first-party client always sends it, which is why the official
// product works through the same endpoint.
//
// The rewrite is an append-only byte splice: every original byte, including a
// caller's own metadata object, keeps its position. Only an absent root
// metadata field is added; a present object (or explicit null) is the caller's
// own billing decision and stays untouched. The upstream ignores client_name
// alone, a root-level call_source and an empty metadata object (measured
// 402), so an injection must carry call_source to mean anything.
//
// Scope is Chat and Responses: measured 200/402/200 on Chat, Responses and
// Embeddings for the same three keys, while token counting and FIM are not
// verified and stay untouched.
//
// With mode chat-key the decision belongs to the sending account, so this runs
// in the attempt after selection and uses the workspace's read key type rather
// than the key string: an unread key is unknown and is not assumed to be Vibe.
func applyMistralPlanKey(body []byte, path string, t config.MistralChatMetadata, acct config.Account, snap *quota.Snapshot) ([]byte, bool) {
	if !mistralPlanKeyPath(path) {
		return body, false
	}
	switch t.Mode {
	case config.MistralChatMetadataAllKeys:
	case config.MistralChatMetadataChatKey:
		if !mistralVibeKey(acct, snap) {
			return body, false
		}
	default:
		return body, false
	}
	return appendMistralPlanKeyMetadata(body, t)
}

// mistralPlanKeyPath lists the generation endpoints where the measured
// call_source routing applies. Token counting and FIM returned no evidence and
// therefore keep the caller's bytes exactly.
func mistralPlanKeyPath(path string) bool {
	return path == "/v1/chat/completions" || path == "/v1/responses"
}

// appendMistralPlanKeyMetadata performs the append-only splice. A body that
// already has a root metadata field is returned unchanged.
func appendMistralPlanKeyMetadata(body []byte, t config.MistralChatMetadata) ([]byte, bool) {
	open := skipSpace(body, 0)
	if open >= len(body) || body[open] != '{' {
		return body, false
	}
	o := scanMistralObject(body)
	if o == nil || o.get("metadata") != nil {
		return body, false
	}
	field := append([]byte(`"metadata":`), mistralPlanKeyTemplate(t)...)
	out := make([]byte, 0, len(body)+len(field)+1)
	out = append(out, o.body[:o.end]...)
	if len(o.fields) > 0 {
		out = append(out, ',')
	}
	out = append(out, field...)
	out = append(out, o.body[o.end:]...)
	return out, true
}

// mistralVibeKey reports whether this account's key was read as a CHAT (Vibe)
// key. The type comes only from the workspace quota read.
func mistralVibeKey(acct config.Account, snap *quota.Snapshot) bool {
	if acct.Provider != "" && acct.Provider != config.ProviderMistral {
		return false
	}
	return snap != nil && snap.Mistral != nil && snap.Mistral.KeyType == "CHAT"
}

// mistralPlanKeyTemplate renders the constrained template as a JSON object.
// Values were validated at configuration time, so only known literals appear.
func mistralPlanKeyTemplate(t config.MistralChatMetadata) []byte {
	var b bytes.Buffer
	b.WriteString(`{"call_source":`)
	source, _ := json.Marshal(t.CallSource)
	b.Write(source)
	if t.ClientName != "" {
		name, _ := json.Marshal(t.ClientName)
		b.WriteString(`,"client_name":`)
		b.Write(name)
	}
	b.WriteByte('}')
	return b.Bytes()
}

func mistralString(raw []byte) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

// Native Chat rejects even store:false for the three exact reasoning models.
// Only omit values that don't request persistence; leave metadata and history untouched.
func adaptMistralChatStore(o *mistralWireObject) error {
	if err := o.unique("store"); err != nil {
		return errors.New("Mistral Chat is stateless: duplicate store is not supported")
	}
	if store := o.get("store"); store != nil {
		if !bytes.Equal(store, []byte("false")) && !bytes.Equal(store, []byte("null")) {
			return errors.New("Mistral Chat is stateless: omit store or use false/null; store=true is not supported")
		}
		o.set("store", nil)
	}
	return nil
}

func adaptMistralDeepSeekParameters(o *mistralWireObject) error {
	// Observed DeepSeek Chat schema: temperature=1.62 is rejected with
	// less_than_equal/ctx.le=1.5. Validate before admission, never silently
	// clamp sampling intent or apply this bound to unverified models/paths.
	if err := o.unique("temperature"); err != nil {
		return err
	}
	if raw := o.get("temperature"); raw != nil && !bytes.Equal(raw, []byte("null")) {
		var temperature float64
		if json.Unmarshal(raw, &temperature) != nil || temperature > 1.5 {
			return errors.New("Mistral DeepSeek Chat temperature must be a number no greater than 1.5 or null; lower temperature or omit it; sampling was not changed")
		}
	}
	if err := o.unique("messages", "seed", "random_seed", "logit_bias", "verbosity", "user", "safety_identifier", "prompt_cache_retention", "prompt_cache_key", "metadata"); err != nil {
		return err
	}
	if seed := o.get("seed"); seed != nil {
		if !bytes.Equal(seed, []byte("null")) {
			n, valid := mistralSeed(seed)
			if !valid {
				return errors.New("Mistral Chat seed must be an integer or null; reproducibility cannot be silently discarded")
			}
			if native := o.get("random_seed"); native != nil {
				other, ok := mistralSeed(native)
				if !ok || n.Cmp(other) != 0 {
					return errors.New("Mistral Chat seed conflicts with explicit random_seed; neither value was overwritten")
				}
			} else {
				o.set("random_seed", seed)
			}
		}
		o.set("seed", nil)
	}
	if bias := o.get("logit_bias"); bias != nil {
		empty := bytes.Equal(bias, []byte("null"))
		if obj := scanMistralObject(bias); obj != nil && len(obj.fields) == 0 {
			empty = true
		}
		if !empty {
			return errors.New("Mistral DeepSeek Chat does not support nonempty logit_bias; requested token bias was not discarded")
		}
		o.set("logit_bias", nil)
	}
	if v := o.get("verbosity"); v != nil {
		if !bytes.Equal(v, []byte("null")) && mistralString(v) != "medium" {
			return errors.New("Mistral DeepSeek Chat does not support explicit verbosity; only null/default medium may be omitted")
		}
		o.set("verbosity", nil)
	}
	for _, key := range []string{"user", "safety_identifier"} {
		if v := o.get(key); v != nil {
			if !bytes.Equal(v, []byte("null")) && v[0] != '"' {
				return fmt.Errorf("Mistral Chat %s must be a string or null; it is only a compatibility label, not an upstream tracking/safety feature", key)
			}
			o.set(key, nil)
		}
	}
	if v := o.get("prompt_cache_retention"); v != nil {
		// OpenAI clients send these as cache-performance hints. Native Chat
		// has no retention field: keep its normal best-effort caching and
		// existing cache key, without promising a TTL or data-storage policy.
		var retention *string
		if err := json.Unmarshal(v, &retention); err != nil || retention != nil && *retention != "" && *retention != "in_memory" && *retention != "24h" {
			return errors.New("Mistral DeepSeek Chat prompt_cache_retention must be in_memory, 24h, empty or null; cache retention is best-effort")
		}
		o.set("prompt_cache_retention", nil)
	}
	return nil
}

func mistralReasoningModel(model string) bool {
	switch model {
	case "deepseek-v4-1-flash-1017", "zai-glm-5-3", "zai-glm-5-3-flash":
		return true
	default:
		return false
	}
}

// Only shared reasoning controls belong here. Keep Chat store compatibility
// separate, and don't apply unrelated DeepSeek fields or rewrite Responses input.
func adaptMistralReasoningParameters(o *mistralWireObject, responses bool) error {
	if err := o.unique("reasoning_effort", "reasoning", "prompt_cache_key", "metadata"); err != nil {
		return err
	}
	if !responses {
		if err := o.unique("messages", "thinking", "enable_thinking"); err != nil {
			return err
		}
		if raw := o.get("thinking"); raw != nil {
			thinking := scanMistralObject(raw)
			if thinking == nil || len(thinking.fields) != 1 || thinking.fields[0].key != "type" {
				return errors.New("Mistral Chat thinking requires only type=enabled/disabled; budgets and unknown structures cannot be preserved")
			}
			kind := mistralString(thinking.get("type"))
			if kind != "enabled" && kind != "disabled" {
				return errors.New("Mistral Chat thinking.type must be enabled or disabled")
			}
			o.set("thinking", nil)
		}
		if raw := o.get("enable_thinking"); raw != nil {
			if !bytes.Equal(raw, []byte("true")) && !bytes.Equal(raw, []byte("false")) {
				return errors.New("Mistral Chat enable_thinking must be boolean")
			}
			o.set("enable_thinking", nil)
		}
	}

	flat := o.get("reasoning_effort")
	effort := mistralReasoningEffort(flat)
	raw := o.get("reasoning")
	var reasoning *mistralWireObject
	if raw != nil && !(responses && bytes.Equal(raw, []byte("null"))) {
		reasoning = scanMistralObject(raw)
		if reasoning == nil {
			return errors.New("Mistral reasoning requires only effort for Chat, or a native reasoning object/null for Responses")
		}
		if err := reasoning.unique("effort"); err != nil {
			return err
		}
		if !responses && (len(reasoning.fields) != 1 || reasoning.fields[0].key != "effort") {
			return errors.New("Mistral Chat reasoning requires only effort; unknown structures and additional fields cannot be preserved")
		}
		if nested := reasoning.get("effort"); nested != nil {
			alias := mistralReasoningEffort(nested)
			if flat != nil && mistralString(effort) != mistralString(alias) {
				return errors.New("Mistral reasoning.effort conflicts with explicit reasoning_effort")
			}
			if flat == nil {
				effort = alias
			}
		}
	}
	if responses {
		if reasoning == nil {
			o.set("reasoning", append(append([]byte(`{"effort":`), effort...), '}'))
		} else if !bytes.Equal(reasoning.get("effort"), effort) {
			reasoning.set("effort", effort)
			o.set("reasoning", reasoning.render()) // preserve summary and all other native fields
		}
		if flat != nil {
			o.set("reasoning_effort", nil)
		}
	} else {
		if !bytes.Equal(flat, effort) {
			o.set("reasoning_effort", effort)
		}
		if raw != nil {
			o.set("reasoning", nil)
		}
	}
	return nil
}

func mistralReasoningEffort(raw []byte) []byte {
	switch mistralString(raw) {
	case "low", "high", "max":
		return raw
	default:
		return []byte(`"high"`)
	}
}

func mistralSeed(raw []byte) (*big.Int, bool) {
	// JSON was validated already. Do not round through float64 or accept a
	// fractional/exponential representation that would require rewriting it.
	if len(raw) == 0 || (raw[0] != '-' && (raw[0] < '0' || raw[0] > '9')) || bytes.ContainsAny(raw, ".eE") {
		return nil, false
	}
	return new(big.Int).SetString(string(raw), 10)
}

func mistralChatMessages(raw []byte, deepSeek, reasoningAlias bool) ([]byte, bool, error) {
	if len(raw) == 0 || raw[0] != '[' {
		return raw, false, nil // native validation owns unrelated malformed messages
	}
	var out bytes.Buffer
	changed, last := false, 0
	for i := skipSpace(raw, 1); raw[i] != ']'; {
		end := skipValue(raw, i)
		message := scanMistralObject(raw[i:end])
		if message != nil {
			role := mistralString(message.get("role"))
			if deepSeek || reasoningAlias || message.get("reasoning_content") != nil {
				if err := message.unique("role"); err != nil {
					return nil, false, err
				}
			}
			// GLM also rejects the developer role. Share only this instruction
			// alias; its other native parameters/content stay outside DeepSeek cleanup.
			if (deepSeek || reasoningAlias) && role == "developer" {
				message.set("role", []byte(`"system"`))
			}
			if deepSeek && role == "user" {
				if err := mistralUserImages(message); err != nil {
					return nil, false, err
				}
			}
			if deepSeek && (role == "user" || role == "assistant") {
				if err := mistralDeepSeekTextChunks(message, role == "assistant"); err != nil {
					return nil, false, err
				}
			}
			if role == "assistant" {
				if deepSeek {
					if err := mistralDeepSeekToolCalls(message); err != nil {
						return nil, false, err
					}
				}
				reasoning := message.get("reasoning_content")
				// All three reasoning models use Mistral's native typed chunks.
				// Preserve legacy assistant reasoning via the same representation.
				if alias := message.get("reasoning"); reasoningAlias && alias != nil {
					if err := message.unique("reasoning", "reasoning_content", "content"); err != nil {
						return nil, false, err
					}
					var aliased, canonical *string
					if json.Unmarshal(alias, &aliased) != nil {
						return nil, false, errors.New("Mistral assistant reasoning must be a string or null")
					}
					if reasoning != nil && json.Unmarshal(reasoning, &canonical) != nil {
						return nil, false, errors.New("Mistral assistant reasoning_content must be a string or null")
					}
					// Some relays replay a space-only canonical placeholder next to
					// the full alias. Compare meaningful thoughts, but never trim
					// the selected original text or prefer one real conflict.
					aliasHasThought := aliased != nil && strings.TrimSpace(*aliased) != ""
					canonicalHasThought := canonical != nil && strings.TrimSpace(*canonical) != ""
					if aliasHasThought && canonicalHasThought && *aliased != *canonical {
						return nil, false, errors.New("Mistral assistant reasoning conflicts with reasoning_content; neither thought was discarded")
					}
					if reasoning == nil || canonical == nil || !canonicalHasThought {
						reasoning = alias
					}
					message.set("reasoning", nil)
				}
				if reasoning != nil {
					if err := message.unique("content", "reasoning_content"); err != nil {
						return nil, false, err
					}
					if err := mistralAssistantReasoning(message, reasoning, reasoningAlias); err != nil {
						return nil, false, err
					}
				}
			}
			if message.changed() {
				if !changed {
					// Unchanged histories keep the original slice. Once changed,
					// splice directly into the array, not a per-message copy.
					out.Grow(len(raw) + len(raw)/8 + 1024)
				}
				out.Write(raw[last:i])
				message.writeSplice(&out)
				last, changed = end, true
			}
		}
		i = skipSpace(raw, end)
		if raw[i] == ',' {
			i = skipSpace(raw, i+1)
		}
	}
	if !changed {
		return raw, false, nil
	}
	out.Write(raw[last:])
	return out.Bytes(), true, nil
}

// Mistral's native vision form accepts a string URL. Remove only empty
// NewAPI MimeType metadata, then unwrap URL-only objects; retain real options.
func mistralUserImages(message *mistralWireObject) error {
	content := message.get("content")
	if len(content) == 0 || content[0] != '[' {
		return nil
	}
	if err := message.unique("content"); err != nil {
		return err
	}
	if !bytes.Contains(content, []byte(`"image_url"`)) && !bytes.Contains(content, []byte(`\u`)) {
		return nil // pure-text content arrays need no image decoding or copies
	}
	var chunks []json.RawMessage
	_ = json.Unmarshal(content, &chunks)
	changed := false
	for i, raw := range chunks {
		chunk := scanMistralObject(raw)
		if chunk == nil || mistralString(chunk.get("type")) != "image_url" {
			continue
		}
		if err := chunk.unique("type", "image_url"); err != nil {
			return err
		}
		image := scanMistralObject(chunk.get("image_url"))
		if image == nil {
			continue // already native or unrelated malformed input: retain upstream validation
		}
		if err := image.unique("url", "MimeType"); err != nil {
			return err
		}
		fields := len(image.fields)
		mime := image.get("MimeType")
		if bytes.Equal(mime, []byte(`""`)) || bytes.Equal(mime, []byte("null")) {
			// NewAPI may leak this empty internal field; Mistral rejects it.
			image.set("MimeType", nil)
			fields--
			chunk.set("image_url", image.render())
			chunks[i], changed = chunk.render(), true
		}
		if fields != 1 || image.get("url") == nil {
			continue // never drop detail or any other requested option
		}
		url := image.get("url")
		if len(url) == 0 || url[0] != '"' {
			return errors.New("Mistral DeepSeek user image_url.url must be a string")
		}
		chunk.set("image_url", url)
		chunks[i], changed = chunk.render(), true
	}
	if changed {
		content, _ = json.Marshal(chunks)
		message.set("content", content)
	}
	return nil
}

// Compare decoded text exactly, without trimming or rebuilding native chunks.
// Equal legacy/native representations describe one thought, not two. Preserve
// the native order, text-part boundaries, closed flags and original JSON bytes.
func mistralNativeThoughtMatches(chunks []json.RawMessage, text string) bool {
	found, offset := false, 0
	for _, raw := range chunks {
		view := scanMistralObject(raw)
		if view == nil {
			return false
		}
		if mistralString(view.get("type")) != "thinking" {
			continue
		}
		if view.unique("type", "thinking", "closed", "signature") != nil {
			return false
		}
		var rawParts []json.RawMessage
		if json.Unmarshal(view.get("thinking"), &rawParts) != nil {
			return false
		}
		for _, part := range rawParts {
			p := scanMistralObject(part)
			if p == nil || p.unique("type", "text") != nil {
				return false
			}
		}
		var chunk map[string]json.RawMessage
		if json.Unmarshal(raw, &chunk) != nil {
			return false
		}
		parts, err := mistralThinkingTextParts(chunk)
		if err != nil {
			return false
		}
		for _, part := range parts {
			if !strings.HasPrefix(text[offset:], part) {
				return false
			}
			offset += len(part)
		}
		found = true
	}
	return found && offset == len(text)
}

func mistralAssistantReasoning(message *mistralWireObject, reasoning []byte, preserveNative bool) error {
	var text string
	if json.Unmarshal(reasoning, &text) != nil {
		return errors.New("Mistral assistant reasoning_content must be a string or null")
	}
	if text != "" {
		var answer []byte
		var existing []json.RawMessage
		content := message.get("content")
		isArray := len(content) != 0 && content[0] == '['
		switch {
		case len(content) == 0 || bytes.Equal(content, []byte("null")):
		case content[0] == '"':
			answer, _ = json.Marshal(mistralString(content))
		case isArray:
			_ = json.Unmarshal(content, &existing)
			if preserveNative && mistralNativeThoughtMatches(existing, text) {
				message.set("reasoning_content", nil)
				return nil
			}
		default:
			return errors.New("Mistral assistant content must be text, chunks or null to preserve reasoning")
		}

		// Match encoding/json's historical sorted keys and escaping. Write the
		// scalar case into one array instead of allocating the two chunks first.
		const thinkingStart, thinkingEnd = `{"thinking":[{"text":`, `,"type":"text"}],"type":"thinking"}`
		const answerStart, answerEnd = `,{"text":`, `,"type":"text"}`
		quoted, _ := json.Marshal(text)
		size := 2 + len(thinkingStart) + len(quoted) + len(thinkingEnd)
		if answer != nil {
			size += len(answerStart) + len(answer) + len(answerEnd)
		}
		joined := make([]byte, 0, size)
		joined = append(joined, '[')
		joined = append(joined, thinkingStart...)
		joined = append(joined, quoted...)
		joined = append(joined, thinkingEnd...)
		if isArray {
			// The established array encoder compacts the existing chunks too.
			joined, _ = json.Marshal(append([]json.RawMessage{joined[1:]}, existing...))
		} else {
			if answer != nil {
				joined = append(joined, answerStart...)
				joined = append(joined, answer...)
				joined = append(joined, answerEnd...)
			}
			joined = append(joined, ']')
		}
		message.set("content", joined)
	}
	message.set("reasoning_content", nil)
	return nil
}
