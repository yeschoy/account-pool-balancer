package factory

// StripOpenAISampling removes temperature and top_p for a route that serves an
// OpenAI reasoning model: OpenAI answers them with 400, and droid sends neither.
// magpie's droidBody drops them on the Responses route for the same reason.
func StripOpenAISampling(body []byte) ([]byte, bool) {
	var m object
	if decode(body, &m) != nil || m == nil {
		return body, false
	}
	changed := false
	for _, key := range []string{"temperature", "top_p"} {
		if _, ok := m[key]; ok {
			delete(m, key)
			changed = true
		}
	}
	if !changed {
		return body, false
	}
	out, err := marshal(m)
	if err != nil {
		return body, false
	}
	return out, true
}
