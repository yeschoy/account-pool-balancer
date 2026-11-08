package server

import (
	"bytes"
	"encoding/json"
	"errors"
)

// normalizeMistralCacheAlias runs on strictly validated JSON, before identity
// selection. Only OpenCode's Chat alias is consumed; unrelated wire fields and
// an existing canonical value (including null/empty) are never re-encoded.
func normalizeMistralCacheAlias(body []byte, path string) ([]byte, error) {
	if path != "/v1/chat/completions" {
		return body, nil
	}
	o := scanMistralObject(body)
	alias := o.get("promptCacheKey")
	if alias == nil {
		return body, nil
	}
	if err := o.unique("promptCacheKey", "prompt_cache_key"); err != nil {
		return nil, err
	}
	decode := func(raw []byte) (*string, error) {
		var value *string
		if json.Unmarshal(raw, &value) != nil {
			return nil, errors.New("Mistral Chat cache key must be a string or null")
		}
		return value, nil
	}
	value, err := decode(alias)
	if err != nil {
		return nil, err
	}
	if canonical := o.get("prompt_cache_key"); canonical != nil {
		other, err := decode(canonical)
		if err != nil {
			return nil, err
		}
		if (value == nil) != (other == nil) || value != nil && *value != *other {
			return nil, errors.New("Mistral Chat promptCacheKey conflicts with prompt_cache_key; neither value was overwritten")
		}
	} else {
		o.set("prompt_cache_key", bytes.Clone(alias))
	}
	o.set("promptCacheKey", nil)
	return o.render(), nil
}
