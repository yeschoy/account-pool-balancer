package searchgateway

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"

	"account-pool-balancer/internal/websearch"
)

type usageSum struct {
	valid                             bool
	rounds                            int
	in, out, total, cached, reasoning int64
	allCached, allReasoning           bool
}

func newUsageSum() usageSum { return usageSum{valid: true, allCached: true, allReasoning: true} }
func intField(o object, k string) (int64, bool) {
	var n int64
	if json.Unmarshal(o[k], &n) != nil || n < 0 {
		return 0, false
	}
	return n, true
}
func addInt(dst *int64, n int64) bool {
	if n > math.MaxInt64-*dst {
		return false
	}
	*dst += n
	return true
}
func (u *usageSum) add(raw json.RawMessage) {
	u.rounds++
	o, e := decodeObject(raw)
	if e != nil {
		u.valid = false
		return
	}
	for _, v := range []struct {
		k   string
		dst *int64
	}{{"input_tokens", &u.in}, {"output_tokens", &u.out}, {"total_tokens", &u.total}} {
		n, ok := intField(o, v.k)
		if !ok || !addInt(v.dst, n) {
			u.valid = false
		}
	}
	for _, v := range []struct {
		k, field string
		dst      *int64
		valid    *bool
	}{{"input_tokens_details", "cached_tokens", &u.cached, &u.allCached}, {"output_tokens_details", "reasoning_tokens", &u.reasoning, &u.allReasoning}} {
		d, e := decodeObject(o[v.k])
		if e != nil {
			*v.valid = false
			continue
		}
		n, ok := intField(d, v.field)
		if !ok || !addInt(v.dst, n) {
			*v.valid = false
		}
	}
}
func (u usageSum) json() json.RawMessage {
	if !u.valid || u.rounds == 0 {
		return nil
	}
	o := map[string]any{"input_tokens": u.in, "output_tokens": u.out, "total_tokens": u.total}
	if u.allCached {
		o["input_tokens_details"] = map[string]int64{"cached_tokens": u.cached}
	}
	if u.allReasoning {
		o["output_tokens_details"] = map[string]int64{"reasoning_tokens": u.reasoning}
	}
	return marshal(o)
}

func (g *Gateway) run(r *http.Request, route Route, p plan, e *emitter) error {
	totals := newUsageSum()
	searches := 0
	var remaining int64
	if raw := p.body["max_output_tokens"]; raw != nil && string(raw) != "null" {
		if json.Unmarshal(raw, &remaining) != nil || remaining < 1 {
			return invalid("invalid_output_budget", "max_output_tokens must be a positive integer")
		}
	}
	for round := 0; round <= g.cfg.MaxSearches; round++ {
		if err := r.Context().Err(); err != nil {
			return err
		}
		p.body["input"] = marshal(p.input)
		p.body["stream"] = marshal(p.stream)
		if remaining > 0 {
			p.body["max_output_tokens"] = marshal(remaining)
		}
		re := newRoundEmitter(e)
		result, err := g.round(r, route, p, re)
		if err != nil {
			return err
		}
		e.last = result.response
		totals.add(result.usage)
		e.usage = totals.json()
		e.begin()
		hasClientCall := false
		calls := []json.RawMessage{}
		for i, raw := range result.output {
			item, _ := decodeObject(raw)
			if str(item["type"]) == "function_call" && str(item["name"]) == searchFunction {
				calls = append(calls, raw)
				continue
			}
			if str(item["type"]) == "function_call" {
				hasClientCall = true
			}
			re.finalItem(i, item)
		}
		if e.writeErr != nil {
			return e.writeErr
		}
		if result.status == "incomplete" {
			// A partial function call is not executable. Do not invent a successful
			// hosted search from truncated arguments.
			return e.finish("incomplete")
		}
		if len(calls) == 0 {
			return e.finish("completed")
		}
		if !p.search {
			return upstream("unadvertised_search_call")
		}
		if searches+len(calls) > g.cfg.MaxSearches {
			return &gatewayError{502, "search_call_limit", "Maximum searches per request reached; no further search was executed"}
		}
		// Preserve original output (including reasoning and ordinary function calls)
		// for the next model step. Search outputs are appended after the whole round.
		p.input = append(p.input, result.output...)
		for _, raw := range calls {
			call, _ := decodeObject(raw)
			args, err := decodeObject([]byte(str(call["arguments"])))
			if err != nil || len(args) != 1 || args["query"] == nil || str(call["call_id"]) == "" {
				return upstream("invalid_search_arguments")
			}
			query := str(args["query"])
			if strings.TrimSpace(query) == "" || len(query) > 4096 {
				return upstream("invalid_search_arguments")
			}
			public := object{"id": marshal(id("ws_apb_")), "type": marshal("web_search_call"), "status": marshal("in_progress"), "action": marshal(map[string]any{"type": "search", "query": query})}
			index := e.add(public)
			eventFields := object{"output_index": marshal(index), "item_id": public["id"]}
			e.event("response.web_search_call.in_progress", eventFields)
			e.event("response.web_search_call.searching", eventFields)
			if e.writeErr != nil {
				return e.writeErr
			}
			found, err := g.search.Search(r.Context(), websearch.Request{Query: query})
			if err != nil {
				return err
			}
			searches++
			payload := marshal(map[string]any{"untrusted_web_results": found.Sources})
			output := marshal(map[string]any{"type": "function_call_output", "call_id": str(call["call_id"]), "output": string(payload)})
			public["status"] = marshal("completed")
			if p.includeSources {
				sources := []map[string]string{}
				for _, s := range found.Sources {
					sources = append(sources, map[string]string{"type": "url", "url": s.URL})
				}
				public["action"] = marshal(map[string]any{"type": "search", "query": query, "sources": sources})
			}
			if err = g.history.put(historyEntry{id: str(public["id"]), scope: p.scope, public: marshal(public), call: raw, result: output}); err != nil {
				return err
			}
			p.input = append(p.input, output)
			e.event("response.web_search_call.completed", eventFields)
			e.done(index, public)
		}
		// Ordinary tools are client-executed. Return them instead of generating an
		// invalid next model round with missing client tool results.
		if hasClientCall {
			return e.finish("completed")
		}
		if remaining > 0 {
			u, err := decodeObject(result.usage)
			used, ok := intField(u, "output_tokens")
			if err != nil || !ok {
				return upstream("unmeasured_output_budget")
			}
			if used >= remaining {
				return &gatewayError{502, "output_budget_exhausted", "Output budget exhausted before the search continuation"}
			}
			remaining -= used
		}
		// A forced/required search is satisfied once. Do not force it indefinitely.
		if raw := p.body["tool_choice"]; raw != nil && string(raw) != "\"none\"" {
			p.body["tool_choice"] = marshal("auto")
		}
		p.body["input"] = marshal(p.input)
		if int64(len(marshal(p.body))) > g.cfg.MaxBodyBytes {
			return &gatewayError{413, "continuation_body_limit", "Search continuation exceeds the configured body limit"}
		}
	}
	return &gatewayError{502, "search_round_limit", "Maximum model rounds reached"}
}
