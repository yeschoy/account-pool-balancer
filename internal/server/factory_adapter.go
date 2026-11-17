package server

import (
	"encoding/json"
	"errors"
	"fmt"

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
}

// prepareFactory is run before leasing an account. Unsupported/lossy conversions
// are local 400s without RPM, network, health changes or silent field removal.
// Same-protocol requests retain their bytes unless a documented wire adapter is
// required. No identity strings, task prompts or tool output text are rewritten.
func prepareFactory(cfg *config.Config, path string, body []byte) (*factoryRequestPlan, error) {
	from := opencode.FromPath(path)
	var m map[string]json.RawMessage
	if from == "" || json.Unmarshal(body, &m) != nil || m == nil {
		return nil, errors.New("Factory requires an exact supported endpoint and JSON object")
	}
	var model string
	if json.Unmarshal(m["model"], &model) != nil || model == "" {
		return nil, errors.New("Factory requires a configured model")
	}
	route, ok := cfg.Factory.Model(model)
	if !ok {
		return nil, errors.New("Factory model is not configured")
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
	if len(plan.body) > cfg.Routing.MaxBodyMB<<20 {
		return nil, errors.New("Factory adapted request exceeds configured request-body limit")
	}
	return plan, nil
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
