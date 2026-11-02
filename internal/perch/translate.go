package perch

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// OpenAI ⇄ Perch translation.
//
// Inbound, an OpenAI chat body is close to Perch's wire shape already:
// {role, content}, assistant.tool_calls, {role:"tool", tool_call_id}, and
// {type:"function",function:{…}} tools all pass through. What has to be added
// is the envelope around them — and the one thing Perch insists on: every call
// must begin with Perch's own persona preamble, or the server refuses it with
// perch_surface_required.
//
// Outbound, Perch streams answer_delta / reasoning_delta / tool_call_delta /
// tool_use_end / stream_restart / done, which this file re-emits as OpenAI
// chat.completion.chunk frames.
//
// The header notes this is verified against Perch's own client, and the two
// facts that are easy to get wrong:
//
//   - A manual model pin is THREE fields: preferredModelId, manualModelOptionId
//     (the same value) and strictManual:false. Sending only preferredModelId —
//     or setting strictManual:true — makes Perch serve the account's default
//     model with HTTP 200 and no warning, which is indistinguishable from "pins
//     are ignored".
//   - `answer_delta`, not `text_delta`, carries the assistant's text.

// Message is one turn on the wire. Content is left as `any` because Perch
// accepts both a string and an array of text/image parts.
type Message struct {
	Role       string     `json:"role"`
	Content    any        `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// ToolCall is an assistant turn's tool call, in OpenAI's shape (Perch accepts
// it verbatim).
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Tool is one function definition, in OpenAI's shape.
type Tool map[string]any

// Envelope is the body of /api/perch-terminal/model-call.
type Envelope struct {
	Request RequestEnvelope `json:"request"`
	RunID   string          `json:"runId"`
	// ThreadID / ChatMode are carried because the CLI's own cli-turn record
	// reports them; on model-call they are informational. omitempty keeps the
	// envelope byte-identical for callers that name no thread.
	ThreadID string         `json:"threadId,omitempty"`
	ChatMode string         `json:"chatMode,omitempty"`
	Lane     string         `json:"lane"`
	Surface  string         `json:"clientSurface"`
	Strict   bool           `json:"strictManual"`
	Attrib   map[string]any `json:"attribution,omitempty"`
	AvoidIDs []string       `json:"avoidModelIds"`
	Resend   bool           `json:"resendAfterOutput"`

	// Model selection: exactly one of these shapes is set.
	PreferredModelID    string `json:"preferredModelId,omitempty"`
	ManualModelOptionID string `json:"manualModelOptionId,omitempty"`
	RoostModelChoice    string `json:"roostModelChoice,omitempty"`

	Reasoning any `json:"reasoning,omitempty"`
}

// RequestEnvelope is the inner request.
type RequestEnvelope struct {
	Messages        []Message `json:"messages"`
	Tools           []Tool    `json:"tools,omitempty"`
	ToolChoice      any       `json:"toolChoice,omitempty"`
	MaxTokens       int       `json:"maxTokens,omitempty"`
	MaxOutputTokens int       `json:"maxOutputTokens,omitempty"`
	Temperature     *float64  `json:"temperature,omitempty"`
	ResponseFormat  any       `json:"responseFormat,omitempty"`
	Stop            []string  `json:"stop,omitempty"`
}

// EnvelopeOptions is what the pool decides per request.
type EnvelopeOptions struct {
	// Lane: "code" keeps Perch's own operator persona out of the context while
	// still speaking the CLI surface. The gate prefix is required on every
	// lane, so it is always sent; the lane is what stops the model from
	// introducing itself as Perch's operator.
	Lane string
	// Model is the caller's model name. A name that is a Perch registry option
	// id becomes a manual pin; "auto" or "" lets Perch route.
	Model string
	// RoostTier, when set (standard|standard_max|pro|pro_max), asks Perch to
	// resolve a tier instead of pinning a model.
	RoostTier string
	// Prefix is the surface-gate preamble. Without it every call is refused,
	// so the caller resolves it before building a body.
	Prefix string
	// GateNotice and IdentityGuard are the two notices that defuse the
	// preamble: the notice explains it is inert boilerplate, the guard (last,
	// so it wins) forbids the model from adopting the persona it names.
	GateNotice    string
	IdentityGuard string
	// NeutralSystem replaces the caller's system turn when there is none.
	NeutralSystem string
	// Reasoning, when non-empty (low|medium|high), is forwarded.
	Reasoning string
	// ResponseFormat passes response_format through.
	ResponseFormat any
	// Attribution is Perch's own attribution block; the pool fills it with the
	// account identity so a turn is billed to the right user.
	Attribution map[string]any
	// ThreadID is the conversation this turn belongs to. When the caller has
	// none, the pool derives the CLI's own cwd thread (perch.CwdThreadID):
	// the real client's thread is "cli-cwd-<sha16 of the working dir>" and it
	// sends it on cli-turn, so a pool call that names a fresh random thread
	// every time is the odd one out. Empty keeps the field at its zero value,
	// which is what every caller did before this was added.
	ThreadID string
	// ChatMode is reported in cli-turn ("code"|"chat"). Empty leaves it "".
	ChatMode string
}

// BuildEnvelope turns an OpenAI chat body into Perch's envelope.
func BuildEnvelope(body map[string]any, opt EnvelopeOptions) Envelope {
	raw, _ := body["messages"].([]any)
	messages := NormalizeMessages(raw, opt)

	env := Envelope{
		RunID:    NewRunID(),
		ThreadID: opt.ThreadID,
		ChatMode: opt.ChatMode,
		Lane:     firstNonEmptyStr(opt.Lane, "code"),
		Surface:  "cli",
		Strict:   false,
		Attrib:   opt.Attribution,
		AvoidIDs: []string{},
		Resend:   true,
	}
	env.Request.Messages = messages
	if tools := NormalizeTools(body["tools"]); len(tools) > 0 {
		env.Request.Tools = tools
		env.Request.ToolChoice = firstAny(body["tool_choice"], "auto")
	}
	// max_tokens is the legacy name, max_completion_tokens the current one;
	// Perch's own client sends maxTokens.
	if n := intOf(body["max_tokens"]); n > 0 {
		env.Request.MaxTokens = n
	} else if n := intOf(body["max_completion_tokens"]); n > 0 {
		env.Request.MaxOutputTokens = n
	}
	if f, ok := floatOf(body["temperature"]); ok {
		env.Request.Temperature = &f
	}
	if stops := stringList(body["stop"]); len(stops) > 0 {
		env.Request.Stop = stops
	}
	if rf := opt.ResponseFormat; rf != nil {
		env.Request.ResponseFormat = rf
	} else if rf, ok := body["response_format"]; ok {
		env.Request.ResponseFormat = rf
	}
	if opt.Reasoning != "" {
		env.Reasoning = map[string]any{"effort": opt.Reasoning}
	}

	name := strings.TrimSpace(opt.Model)
	switch {
	case opt.RoostTier != "":
		env.RoostModelChoice = opt.RoostTier
	case name == "" || name == "auto":
		// let Perch route
	case IsRoostChoice(name):
		env.RoostModelChoice = name
	default:
		// All three fields, or Perch silently serves its default model.
		env.PreferredModelID = name
		env.ManualModelOptionID = name
		env.Strict = false
	}
	return env
}

// IsRoostChoice reports a tier name rather than a model pin.
func IsRoostChoice(name string) bool {
	switch name {
	case "standard", "standard_max", "pro", "pro_max":
		return true
	}
	return false
}

// ComposeSystem builds the system turn Perch requires: the gate preamble, the
// notice that it is inert, the caller's own instructions, and last (so it
// outranks everything above) the identity guard.
func ComposeSystem(caller string, opt EnvelopeOptions) string {
	var parts []string
	if p := strings.TrimSpace(opt.Prefix); p != "" {
		parts = append(parts, p)
	}
	if g := strings.TrimSpace(opt.GateNotice); g != "" {
		parts = append(parts, g)
	}
	if c := strings.TrimSpace(caller); c != "" {
		parts = append(parts, c)
	}
	if g := strings.TrimSpace(opt.IdentityGuard); g != "" {
		parts = append(parts, g)
	}
	return strings.Join(parts, "\n\n")
}

// NormalizeMessages converts an OpenAI message list into Perch's.
//
// The gate compares the beginning of the FIRST system message, so the preamble
// goes into every system turn's head (a client may send several) and a
// synthetic system turn is added when the caller sent none — otherwise the
// model would inherit the persona the preamble names with no instruction
// countering it.
func NormalizeMessages(raw []any, opt EnvelopeOptions) []Message {
	out := make([]Message, 0, len(raw)+1)
	sawSystem := false
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role := strings.TrimSpace(str(m["role"]))
		switch role {
		case "system", "developer":
			sawSystem = true
			out = append(out, Message{Role: "system", Content: ComposeSystem(TextOf(m["content"]), opt)})
		case "user":
			out = append(out, Message{Role: "user", Content: UserContent(m["content"])})
		case "assistant":
			msg := Message{Role: "assistant", Content: TextOf(m["content"])}
			if calls := assistantToolCalls(m["tool_calls"]); len(calls) > 0 {
				msg.ToolCalls = calls
			}
			out = append(out, msg)
		case "tool", "function":
			id := firstNonEmptyStr(str(m["tool_call_id"]), str(m["name"]), "call_0")
			out = append(out, Message{Role: "tool", ToolCallID: id, Content: TextOf(m["content"])})
		default:
			// an unknown role is folded into a user turn rather than dropped
			if text := TextOf(m["content"]); text != "" {
				out = append(out, Message{Role: "user", Content: text})
			}
		}
	}
	if !sawSystem && len(out) > 0 {
		neutral := strings.TrimSpace(opt.NeutralSystem)
		out = append([]Message{{Role: "system", Content: ComposeSystem(neutral, opt)}}, out...)
	}
	return out
}

// assistantToolCalls reads a prior assistant turn's tool calls.
func assistantToolCalls(v any) []ToolCall {
	list, _ := v.([]any)
	out := make([]ToolCall, 0, len(list))
	for _, item := range list {
		c, ok := item.(map[string]any)
		if !ok {
			continue
		}
		fn, _ := c["function"].(map[string]any)
		name := firstNonEmptyStr(str(fn["name"]), str(c["name"]))
		if name == "" {
			continue
		}
		call := ToolCall{ID: firstNonEmptyStr(str(c["id"]), NewRunID()), Type: "function"}
		call.Function.Name = name
		call.Function.Arguments = anyToJSON(fn["arguments"])
		out = append(out, call)
	}
	return out
}

// UserContent keeps image parts in Perch's wire shape. A text-only list is
// joined into one string so the upstream sees the same shape it always did.
func UserContent(v any) any {
	list, ok := v.([]any)
	if !ok {
		return TextOf(v)
	}
	var parts []map[string]any
	for _, item := range list {
		part, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch str(part["type"]) {
		case "text", "input_text":
			if t := str(part["text"]); t != "" {
				parts = append(parts, map[string]any{"type": "text", "text": t})
			}
		case "image_url", "input_image":
			url := imageURL(part)
			if url != "" {
				parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}})
			}
		}
	}
	if len(parts) == 0 {
		return TextOf(v)
	}
	allText := true
	texts := make([]string, 0, len(parts))
	for _, p := range parts {
		if p["type"] != "text" {
			allText = false
			break
		}
		texts = append(texts, p["text"].(string))
	}
	if allText {
		return strings.Join(texts, "\n")
	}
	return parts
}

func imageURL(part map[string]any) string {
	switch v := part["image_url"].(type) {
	case string:
		return v
	case map[string]any:
		return str(v["url"])
	}
	if u := str(part["url"]); u != "" {
		return u
	}
	if data := str(part["data"]); data != "" {
		mime := firstNonEmptyStr(str(part["mime_type"]), str(part["mimeType"]), "image/png")
		return "data:" + mime + ";base64," + data
	}
	return ""
}

// NormalizeTools keeps the function definitions Perch accepts.
func NormalizeTools(v any) []Tool {
	list, _ := v.([]any)
	out := make([]Tool, 0, len(list))
	for _, item := range list {
		t, ok := item.(map[string]any)
		if !ok || str(t["type"]) != "function" {
			continue
		}
		fn, _ := t["function"].(map[string]any)
		name := str(fn["name"])
		if name == "" {
			continue
		}
		params := fn["parameters"]
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, Tool{
			"type": "function",
			"function": map[string]any{
				"name":        name,
				"description": firstNonEmptyStr(str(fn["description"])),
				"parameters":  params,
			},
		})
	}
	return out
}

// TextOf flattens a content value to text.
func TextOf(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		var b strings.Builder
		for _, item := range x {
			switch p := item.(type) {
			case string:
				b.WriteString(p)
			case map[string]any:
				if t := str(p["text"]); t != "" {
					b.WriteString(t)
				}
			}
		}
		return b.String()
	default:
		return ""
	}
}

// ---- outbound -----------------------------------------------------------------

// FinishReason maps Perch's finish reason to OpenAI's.
func FinishReason(reason string, hasToolCalls bool) string {
	if hasToolCalls {
		return "tool_calls"
	}
	switch reason {
	case "length", "max_tokens":
		return "length"
	case "tool_calls", "tool_use":
		return "tool_calls"
	case "content_filter":
		return "content_filter"
	default:
		return "stop"
	}
}

// OpenAIToolCalls converts Perch's calls into OpenAI's, giving each an index.
// Ids are kept when Perch supplied one so a downstream tool loop can match the
// result back to the call.
func OpenAIToolCalls(raw []RawToolCall) []map[string]any {
	out := make([]map[string]any, 0, len(raw))
	for i, c := range raw {
		name := c.CallName()
		if name == "" {
			continue
		}
		id := c.ID
		if id == "" {
			id = NewRunID()
		}
		args := c.CallArgs()
		if args == "" {
			args = "{}"
		}
		out = append(out, map[string]any{
			"index": i,
			"id":    id,
			"type":  "function",
			"function": map[string]any{
				"name":      name,
				"arguments": args,
			},
		})
	}
	return out
}

// OpenAIUsage maps Perch's token counts to OpenAI's.
func OpenAIUsage(u *Usage) map[string]any {
	if u == nil {
		return nil
	}
	prompt, completion := u.InputTokens, u.OutputTokens
	total := u.TotalTokens
	if total == 0 {
		total = prompt + completion
	}
	out := map[string]any{
		"prompt_tokens":     prompt,
		"completion_tokens": completion,
		"total_tokens":      total,
	}
	if u.CacheReadInputTokens > 0 {
		out["prompt_tokens_details"] = map[string]any{"cached_tokens": u.CacheReadInputTokens}
	}
	return out
}

// NewRunID makes a run id (Perch groups a turn's telemetry by it).
func NewRunID() string {
	b := randHex(12)
	return "run_" + b
}

// ---- small readers --------------------------------------------------------------

func firstAny(vals ...any) any {
	for _, v := range vals {
		if v != nil {
			return v
		}
	}
	return nil
}

func intOf(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	}
	return 0
}

func floatOf(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

func stringList(v any) []string {
	switch x := v.(type) {
	case string:
		if strings.TrimSpace(x) == "" {
			return nil
		}
		return []string{x}
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func anyToJSON(v any) string {
	switch x := v.(type) {
	case nil:
		return "{}"
	case string:
		if strings.TrimSpace(x) == "" {
			return "{}"
		}
		return x
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return "{}"
		}
		return string(b)
	}
}

// randHex is crypto/rand in hex, used only for run ids.
func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---- the web surface envelope ---------------------------------------------------

// WebMessage is the body of POST /api/perchai/chat: the message the web app
// itself sends when the user types in a thread.
//
// It is far thinner than the CLI envelope, and that is the point: no system
// turn, no tools, no persona preamble. What the web lane cannot express (system
// prompts, tool calls, images, sampling parameters) is folded or dropped by
// BuildWebMessage rather than silently forwarded in a field the endpoint
// ignores.
type WebMessage struct {
	Message         string `json:"message"`
	ClientMessageID string `json:"clientMessageId"`
	ThreadID        string `json:"threadId"`
	WebSearch       bool   `json:"webSearch"`
	// ManualModelOptionID is a registry option id (openrouter-qwen-…). It is
	// always sent: on a plan whose pool serves one model Perch substitutes it
	// silently, and an option id derived from the OpenAI model name is the
	// only chance of a real pin.
	ManualModelOptionID *string `json:"manualModelOptionId"`
	PromoOverflow       bool    `json:"promoOverflowAccepted"`
}

// webHistoryPrefix / webHistoryTurn frame earlier turns inside the one message
// the endpoint accepts. A thread is reused per thread id, so the upstream's
// own history is what the model already saw; this only carries the turns of a
// request that the thread does not know (a client re-sending a conversation,
// or a tool loop).
const (
	webHistoryPrefix = "[earlier turns of this conversation]\n"
	webHistoryTurn   = "%s: %s\n"
)

// BuildWebMessage turns an OpenAI chat body into the web lane's message.
//
// The rules, in order:
//   - every system/developer turn goes first, as plain text: the web lane has
//     no system channel, so this is the only way a caller's instructions reach
//     the model at all;
//   - earlier user/assistant/tool turns follow, labelled, so a stateless
//     caller's conversation survives;
//   - an assistant turn that called tools is rendered as its call (there is no
//     tool channel either), and a tool result as its output;
//   - the last user turn is the message itself — that is what Perch treats as
//     the prompt and what it titles the thread from.
func BuildWebMessage(body map[string]any, threadID string) (WebMessage, bool) {
	raw, _ := body["messages"].([]any)
	var systems, turns []string
	prompt := ""
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		text := strings.TrimSpace(TextOf(m["content"]))
		switch strings.TrimSpace(str(m["role"])) {
		case "system", "developer":
			if text != "" {
				systems = append(systems, text)
			}
		case "assistant":
			if text != "" {
				turns = append(turns, fmt.Sprintf(webHistoryTurn, "assistant", text))
			}
			for _, c := range assistantToolCalls(m["tool_calls"]) {
				turns = append(turns, fmt.Sprintf(webHistoryTurn, "assistant (called "+c.Function.Name+")", c.Function.Arguments))
			}
		case "tool", "function":
			if text != "" {
				turns = append(turns, fmt.Sprintf(webHistoryTurn, "tool result", text))
			}
		default:
			if text == "" {
				continue
			}
			if prompt != "" {
				turns = append(turns, fmt.Sprintf(webHistoryTurn, "user", prompt))
			}
			prompt = text
		}
	}
	if prompt == "" {
		return WebMessage{}, false
	}
	var b strings.Builder
	for _, s := range systems {
		b.WriteString(s)
		b.WriteString("\n\n")
	}
	if len(turns) > 0 {
		b.WriteString(webHistoryPrefix)
		for _, t := range turns {
			b.WriteString(t)
		}
		b.WriteString("[end of earlier turns]\n\n")
	}
	b.WriteString(prompt)

	return WebMessage{
		Message:         b.String(),
		ClientMessageID: "odb-" + randHex(8),
		ThreadID:        threadID,
	}, true
}

// WebOptionID maps an OpenAI model name onto a Perch registry option id for
// the web lane's manualModelOptionId.
//
// Two facts decide what this can honestly do:
//
//   - Option ids are `provider-modelid` (wandb-kimi-k2-6,
//     openrouter-qwen-qwen3-8-max-0902). Perch has no route that lists them
//     for an account, so this only passes through an id the caller already
//     names in that shape.
//   - A plan whose pool serves a single model substitutes any pin silently
//     and answers 200. Measured on a Starter account: the pin is accepted and
//     MiniMax M3 answers anyway. So the pin is a hint, and the served model is
//     whatever the done frame's modelLabel says — which the log carries.
//
// A bare model name ("gpt-4o") therefore sends no pin at all rather than a
// guess that would be substituted without a trace.
func WebOptionID(model string) string {
	name := strings.TrimSpace(model)
	if name == "" || name == "auto" || IsRoostChoice(name) {
		return ""
	}
	// an explicit option id already: a known provider prefix and no spaces
	if i := strings.IndexByte(name, '-'); i > 0 && !strings.ContainsAny(name, " /:") {
		switch name[:i] {
		case "wandb", "openrouter", "azure", "bedrock", "anthropic", "dashscope", "cerebras", "nim", "mantle", "google", "vertex":
			return name
		}
	}
	return ""
}

// WithModelOption fills the pin a caller asked for.
func (w WebMessage) WithModelOption(option string) WebMessage {
	option = strings.TrimSpace(option)
	if option == "" {
		return w
	}
	w.ManualModelOptionID = &option
	return w
}

// ---- what the CLI records about a turn ------------------------------------------

// PromptOf is the whole conversation the caller sent, flattened the way the
// CLI's `prompt` field reads: the system turn first, then each speaker.
func PromptOf(body map[string]any) string {
	raw, _ := body["messages"].([]any)
	var b strings.Builder
	for _, it := range raw {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		t := strings.TrimSpace(TextOf(m["content"]))
		if t == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(str(m["role"]))
		b.WriteString(": ")
		b.WriteString(t)
	}
	return b.String()
}
