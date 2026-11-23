package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/factory"
	"account-pool-balancer/internal/opencode"
)

const factoryFastBeta = "fast-mode-2026-02-01"

type factoryRequestPlan struct {
	from, to opencode.Protocol
	body     []byte
	model    string
	beta     bool
	tools    factory.ToolNames
	// session and messageID are droid's conversation and message identifiers
	// for this request. droid sends both on every model call; they derive from
	// the gateway's own conversation fingerprint, never from the caller.
	session     string
	messageID   string
	traceparent string
	// escaped marks a body whose refused client phrases were rewritten; the
	// caller's text is unchanged after JSON decoding, only the wire bytes are.
	escaped bool
	// adapted marks a body magpie's adapters rewrote (identity lines, pi
	// openings, harness wording); the task instructions are unchanged.
	adapted bool
	// trustedPrompt marks an Amp request opting out of prompt string adapters;
	// it does not guarantee upstream policy acceptance.
	trustedPrompt bool
	// toolAliases maps a renamed tool (alias) to the caller's own name. Factory
	// refuses requests declaring certain tool names, so a refused tool goes out
	// under a name it takes and the reply is renamed back.
	toolAliases map[string]string
	// generate sends a native or converted GenerateContentRequest to Factory.
	generate    bool
	countTokens bool
	stream      bool
	// arrayStream is a :streamGenerateContent caller that did not ask for
	// alt=sse. Google answers that call with one JSON array, so the relay keeps
	// the array shape while still writing each chunk as it arrives.
	arrayStream bool
	chatUsage   bool // Converted Chat SSE includes usage only when requested.
}

// prepareFactory is run before leasing an account. Unsupported/lossy conversions
// are local 400s without RPM, network, health changes or silent field removal.
// Same-protocol requests retain their bytes unless a documented wire adapter is
// required. Task prompts, tool input and tool output text are never rewritten;
// the one body change beyond routing is droid's fixed system opening (see
// factory.OpenBody), which Factory requires of every caller.
func prepareFactory(cfg *config.Config, path string, body []byte) (*factoryRequestPlan, error) {
	return prepareFactoryFor(cfg, path, body, false)
}

// prepareFactoryFor lets Amp callers opt out of prompt string matching
// (see factory.IsAmpClient). The header is a performance hint, not client
// authentication or a guarantee that Factory accepts the prompt.
// Trusted bodies skip magpie's prompt adapters and the refused-phrase escape:
// both scan every string in the body and, on a long agent conversation, cost a
// full decode/encode per request. Tool aliasing, droid's opening and every
// protocol adapter still apply.
func prepareFactoryFor(cfg *config.Config, path string, body []byte, trusted bool) (*factoryRequestPlan, error) {
	plan, err := prepareFactoryPlan(cfg, path, body, trusted)
	if plan != nil {
		plan.trustedPrompt = trusted
	}
	return plan, err
}

func prepareFactoryPlan(cfg *config.Config, path string, body []byte, trusted bool) (*factoryRequestPlan, error) {
	if model, ok := factory.ParseGeminiCountTokens(path); ok {
		return prepareFactoryCountTokens(cfg, model, body, trusted)
	}
	if model, stream, ok := factory.ParseGeminiPath(path); ok {
		return prepareFactoryGenerate(cfg, model, stream, body, "", trusted)
	}
	if path == "/v1/embeddings" {
		return prepareFactoryEmbeddings(cfg, body)
	}
	from := opencode.FromPath(path)
	var m map[string]json.RawMessage
	if from == "" || json.Unmarshal(body, &m) != nil || m == nil {
		return nil, errors.New("Factory requires an exact supported endpoint and JSON object")
	}
	var model string
	if json.Unmarshal(m["model"], &model) != nil || model == "" {
		return nil, errors.New("Factory requires a configured model")
	}
	requested := model
	var ok bool
	if model, ok = resolveFactoryAuto(cfg, model, string(from)); !ok {
		return nil, fmt.Errorf("Factory %s has no configured %s model; add one to the Factory model routes", requested, from)
	}
	if model != requested {
		rewritten, err := replaceFactoryModel(body, model)
		if err != nil {
			return nil, err
		}
		body = rewritten
	}
	route, ok := cfg.Factory.Model(model)
	if !ok {
		return nil, errors.New("Factory model is not configured")
	}
	if route.Protocol == string(opencode.Gemini) {
		if !cfg.Factory.Compatibility {
			return nil, errors.New("Factory cross-protocol conversion is disabled; use /v1beta/models/{model}:generateContent or :streamGenerateContent")
		}
		converted, stream, chatUsage, err := opencode.ConvertGeminiRequest(from, body)
		if err != nil {
			return nil, fmt.Errorf("Factory Gemini conversion: %w", err)
		}
		plan, err := prepareFactoryGenerate(cfg, model, stream, converted, from, trusted)
		if err != nil {
			return nil, err
		}
		plan.chatUsage = chatUsage
		return plan, nil
	}
	to := opencode.Protocol(route.Protocol)
	plan := &factoryRequestPlan{from: from, to: to, body: body, model: model}
	if from != to {
		if !cfg.Factory.Compatibility {
			return nil, errors.New("Factory cross-protocol conversion is disabled; use the model's native endpoint")
		}
		converted, err := opencode.ConvertRequest(from, to, body)
		if err != nil {
			return nil, fmt.Errorf("Factory protocol conversion: %w", err)
		}
		plan.body = converted
		m = nil
		_ = json.Unmarshal(converted, &m)
	}
	changed := false
	var speed, tier string
	_ = json.Unmarshal(m["speed"], &speed)
	_ = json.Unmarshal(m["service_tier"], &tier)
	askedFast := to == opencode.Messages && speed == "fast" || to == opencode.Responses && tier == "priority"
	if askedFast {
		if fast, exists := cfg.Factory.Model(model + "-fast"); exists && fast.Protocol == route.Protocol && fast.Upstream == route.Upstream && factoryFastModel(fast) {
			plan.model, route = fast.ID, fast
			m["model"], _ = json.Marshal(plan.model)
			changed = true
		}
	}
	// A suffix on a custom model is insufficient: only reference Fast Mode
	// entries are adapted. GLM-5.2 Fast is a separate Chat model, not a tier.
	fast := factoryFastModel(route)
	if fast && to == opencode.Messages {
		plan.beta = true
		if speed != "fast" {
			m["speed"] = json.RawMessage(`"fast"`)
			changed = true
		}
	}
	if fast && to == opencode.Responses && tier != "priority" {
		m["service_tier"] = json.RawMessage(`"priority"`)
		changed = true
	}
	if changed {
		var err error
		plan.body, err = json.Marshal(m)
		if err != nil {
			return nil, err
		}
	}
	if to == opencode.Messages {
		var err error
		plan.body, plan.tools, err = factory.WrapTools(plan.body)
		if err != nil {
			return nil, err
		}
	}
	// Factory refuses any system channel that does not open with droid's line:
	// a measured body with no system at all, and one whose system is an
	// ordinary prompt, are both 403 content_policy_rejected. Every mode but
	// keep therefore ends with the opening in place; keep sends what the
	// caller wrote and accepts that Factory may refuse it.
	if to == opencode.Responses && route.Upstream == "openai" {
		// OpenAI's reasoning models refuse temperature/top_p with 400 and droid
		// sends neither; magpie drops them on this route for the same reason.
		if stripped, changed := factory.StripOpenAISampling(plan.body); changed {
			plan.body = stripped
		}
	}
	mode := cfg.Factory.EffectiveSystemMode()
	adaptPrompt := mode == config.SystemMagpie && !trusted
	switch {
	case mode == config.SystemFold:
		plan.body = factory.FoldSystemIntoUser(string(to), plan.body)
	case mode == config.SystemKeep:
		// unchanged
	case to == opencode.Chat:
		plan.body, plan.toolAliases, plan.adapted = factory.PrepareChatBody(plan.body, adaptPrompt, cfg.Factory.EffectiveRenameAllTools())
	default:
		if adaptPrompt {
			// magpie's adapters rewrite only the fixed client sentences and
			// keep every task instruction.
			if adapted, changed := factory.MagpieAdapt(string(to), plan.body); changed {
				plan.body = adapted
				plan.adapted = true
			}
		}
		// Factory refuses a request declaring certain tool names outright, so a
		// refused tool is renamed before the opening and renamed back in the
		// reply. This is independent of the prompt adapters.
		// One pass: renaming all is a superset of the refused-name pass, and
		// each pass decodes and re-encodes the whole conversation.
		var aliased []byte
		var aliases map[string]string
		if cfg.Factory.EffectiveRenameAllTools() {
			aliased, aliases = factory.AliasToolNamesRenamingAll(plan.body)
		} else {
			aliased, aliases = factory.AliasToolNames(plan.body)
		}
		if len(aliases) > 0 {
			plan.body = aliased
			plan.toolAliases = aliases
		}
		// Guarantee the opening the gateway requires, whatever shape the
		// caller's system channel arrived in (message, string, block list, or
		// absent entirely).
		plan.body = factory.OpenDroidBody(string(to), plan.body)
	}
	// A conversation that quotes another agent's prompt is refused wherever it
	// appears, so the phrases are escaped before the request leaves.
	if cfg.Factory.EffectiveEscapeClientPhrases() && !trusted {
		if escaped, changed := factory.EscapeClientPhrases(plan.body); changed {
			plan.body = escaped
			plan.escaped = true
		}
	}
	if len(plan.body) > cfg.Routing.MaxBodyMB<<20 {
		return nil, errors.New("Factory adapted request exceeds configured request-body limit")
	}
	return plan, nil
}

// prepareFactoryGenerate applies the same Factory wire processing to native
// Gemini requests and requests already converted to GenerateContentRequest.
// A nonempty from marks cross-protocol requests, which reject lossy schemas.
func prepareFactoryGenerate(cfg *config.Config, model string, stream bool, body []byte, from opencode.Protocol, trusted bool) (*factoryRequestPlan, error) {
	requested := model
	resolved, ok := resolveFactoryAuto(cfg, model, "generate")
	if !ok {
		return nil, fmt.Errorf("Factory %s has no configured generate model; add one to the Factory model routes", requested)
	}
	if resolved != requested {
		rewritten, err := replaceFactoryModel(body, resolved)
		if err != nil {
			return nil, err
		}
		body, model = rewritten, resolved
	}
	route, ok := cfg.Factory.Model(model)
	if !ok {
		return nil, errors.New("Factory model is not configured")
	}
	if route.Protocol != "generate" || route.Upstream != "google" {
		return nil, errors.New("Factory model is not a Google generate route")
	}
	mode := cfg.Factory.EffectiveSystemMode()
	var aliases map[string]string
	if mode != config.SystemKeep {
		// When aliasing needs to rewrite the body, normalize the same decoded
		// tool declarations in that pass. The reply still restores each name.
		body, aliases = factory.AliasGeminiToolNames(body, cfg.Factory.EffectiveRenameAllTools())
	}
	level, ok := cfg.Factory.NormalizeThinkingLevel()
	if !ok {
		level = ""
	}
	b, err := factory.PrepareGeminiGenerateBody(model, body, factory.GeminiGenerateOptions{
		FoldSystem:      mode == config.SystemFold,
		AdaptSystem:     mode == config.SystemMagpie && !trusted,
		OpenSystem:      mode != config.SystemFold && mode != config.SystemKeep,
		IncludeThoughts: from == "" && cfg.Factory.EffectiveIncludeThoughts(),
		ThinkingLevel:   level,
		SanitizeTools:   len(aliases) == 0,
	})
	if err != nil {
		return nil, err
	}
	// Keep the original whole-body phrase handling after all wire adapters.
	if cfg.Factory.EffectiveEscapeClientPhrases() && mode != config.SystemKeep && !trusted {
		if escaped, changed := factory.EscapeClientPhrases(b); changed {
			b = escaped
		}
	}
	if len(b) > cfg.Routing.MaxBodyMB<<20 {
		return nil, errors.New("Factory adapted request exceeds configured request-body limit")
	}
	plan := &factoryRequestPlan{body: b, model: model, generate: true, stream: stream, toolAliases: aliases}
	if from != "" {
		plan.from, plan.to = from, opencode.Gemini
	}
	return plan, nil
}

// prepareFactoryEmbeddings accepts OpenAI's embeddings body unchanged. Factory
// serves the same shape at /api/llm/o/v1/embeddings; no system prompt, tool or
// protocol conversion applies.
func prepareFactoryEmbeddings(cfg *config.Config, body []byte) (*factoryRequestPlan, error) {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil || m == nil {
		return nil, errors.New("Factory embeddings requires a JSON object")
	}
	var model string
	if json.Unmarshal(m["model"], &model) != nil || model == "" {
		return nil, errors.New("Factory embeddings requires a configured model")
	}
	requested := model
	var ok bool
	if model, ok = resolveFactoryAuto(cfg, model, "embeddings"); !ok {
		return nil, fmt.Errorf("Factory %s has no configured embeddings model", requested)
	}
	if model != requested {
		rewritten, err := replaceFactoryModel(body, model)
		if err != nil {
			return nil, err
		}
		body = rewritten
	}
	route, ok := cfg.Factory.Model(model)
	if !ok || route.Protocol != "embeddings" || route.Upstream != "openai" {
		return nil, errors.New("Factory embeddings model is not configured")
	}
	if len(body) > cfg.Routing.MaxBodyMB<<20 {
		return nil, errors.New("Factory embeddings request exceeds configured request-body limit")
	}
	return &factoryRequestPlan{body: body, model: model}, nil
}

// resolveFactoryAuto implements the gateway's virtual auto/auto-fast model.
// Droid chooses auto on the client; a downstream client that sends it here
// needs a concrete route, so auto picks the first configured model for this
// downstream protocol and auto-fast picks the first fast variant.
func resolveFactoryAuto(cfg *config.Config, model, protocol string) (string, bool) {
	if model != "auto" && model != "auto-fast" {
		return model, true
	}
	fallback := ""
	for _, m := range cfg.Factory.Models {
		if m.ID == "auto" || m.ID == "auto-fast" || m.Protocol != protocol {
			continue
		}
		if model == "auto-fast" && !strings.HasSuffix(m.ID, "-fast") {
			continue
		}
		if fallback == "" {
			fallback = m.ID
		}
	}
	return fallback, fallback != ""
}

// replaceFactoryModel keeps every other JSON value as raw bytes, including
// integers a float64 round-trip would corrupt.
func replaceFactoryModel(body []byte, model string) ([]byte, error) {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil || m == nil {
		return nil, errors.New("Factory request must be a JSON object")
	}
	b, _ := json.Marshal(model)
	m["model"] = b
	out, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// prepareFactoryCountTokens mirrors the generate body opening so the count
// matches what the generate request would carry, but targets /countTokens.
func prepareFactoryCountTokens(cfg *config.Config, model string, body []byte, trusted bool) (*factoryRequestPlan, error) {
	requested := model
	resolved, ok := resolveFactoryAuto(cfg, model, "generate")
	if !ok {
		return nil, fmt.Errorf("Factory %s has no configured generate model; add one to the Factory model routes", requested)
	}
	if resolved != requested {
		rewritten, err := replaceFactoryModel(body, resolved)
		if err != nil {
			return nil, err
		}
		body, model = rewritten, resolved
	}
	route, ok := cfg.Factory.Model(model)
	if !ok || route.Protocol != "generate" || route.Upstream != "google" {
		return nil, errors.New("Factory model is not a Google generate route")
	}
	body = factory.DropGeminiStreamField(body)
	var b []byte
	var err error
	switch cfg.Factory.EffectiveSystemMode() {
	case config.SystemFold:
		b, err = factory.FoldGeminiSystem(model, body)
	case config.SystemKeep:
		// Only the model placement the captured route needs.
		b, err = factory.OpenGeminiBodyWith(model, body, false)
	default:
		// Factory requires the opening on this route too, so open mode and
		// magpie mode both keep the systemInstruction.
		if cfg.Factory.EffectiveSystemMode() == config.SystemMagpie && !trusted {
			body = factory.MagpieAdaptGemini(body)
		}
		b, err = factory.OpenGeminiBodyWith(model, body, true)
	}
	if err != nil {
		return nil, err
	}
	if len(b) > cfg.Routing.MaxBodyMB<<20 {
		return nil, errors.New("Factory adapted request exceeds configured request-body limit")
	}
	return &factoryRequestPlan{body: b, model: model, countTokens: true}, nil
}

func factoryFastModel(m config.FactoryModel) bool {
	for _, entry := range config.DefaultFactoryModels() {
		if entry == m && m.Protocol != "chat" && len(m.ID) > 5 && m.ID[len(m.ID)-5:] == "-fast" {
			return true
		}
	}
	return false
}

func factoryAdaptationFailure(res *attemptResult) *attemptResult {
	res.kind, res.result, res.status = outFailed, "failed", 502
	res.terminal, res.category, res.contentType = "compatibility", "compatibility", "application/json"
	res.body = []byte(`{"error":{"type":"api_error","code":"factory_tool_adaptation","message":"Factory returned invalid or incomplete wrapped tool arguments; the request was not replayed"}}`)
	return res
}
