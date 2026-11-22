package factory

import (
	"bytes"
	"encoding/json"
	"sort"
	"unicode/utf8"
)

// The Gemini body adapters each touch one or two top-level fields
// (systemInstruction, generationConfig, tools, stream, model), while contents
// carries the whole conversation: on a long agent session that is about 1 MB
// per request. Decoding the full body into a tree for every adapter cost
// several milliseconds each on the request path, so these adapters decode the
// top level only and keep every other value as its original bytes. The
// decoder still validates the whole document; untouched values are written
// back byte-for-byte, including their whitespace (never HTML-escaped).
type topObject map[string]json.RawMessage

// decodeTop decodes a JSON object's top level. A non-object, null or trailing
// data is an error, as with decode.
//
// Validate without allocating a full object tree: the exported adapters also
// accept bodies outside the request handler and must still reject malformed
// nested JSON. The common case then splits the top level with a byte scanner;
// values are slices of b, not copies. Unusual keys fall back to encoding/json.
func decodeTop(b []byte) (topObject, error) {
	if json.Valid(b) {
		if m, ok := splitTop(b); ok {
			return m, nil
		}
	}
	// Unmarshal, not a Decoder: no number handling is needed for raw values,
	// and it validates in place instead of copying the body into a growing
	// read buffer. Trailing data is an error here too.
	var m topObject
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, errNotObject
	}
	return m, nil
}

// splitTop splits an already validated JSON document. It reports false for
// escaped, non-UTF-8 and duplicate keys, leaving those to encoding/json.
func splitTop(b []byte) (topObject, bool) {
	i := skipWS(b, 0)
	if i >= len(b) || b[i] != '{' {
		return nil, false
	}
	m := topObject{}
	i = skipWS(b, i+1)
	if i < len(b) && b[i] == '}' {
		return m, skipWS(b, i+1) == len(b)
	}
	for {
		if i >= len(b) || b[i] != '"' {
			return nil, false
		}
		ke, escaped := scanString(b, i)
		if ke < 0 || escaped {
			return nil, false
		}
		key := string(b[i+1 : ke-1])
		if !utf8.ValidString(key) { // encoding/json would substitute U+FFFD
			return nil, false
		}
		if _, dup := m[key]; dup {
			return nil, false
		}
		i = skipWS(b, ke)
		if i >= len(b) || b[i] != ':' {
			return nil, false
		}
		vs := skipWS(b, i+1)
		ve := scanValue(b, vs)
		if ve < 0 {
			return nil, false
		}
		m[key] = json.RawMessage(b[vs:ve:ve])
		i = skipWS(b, ve)
		if i >= len(b) {
			return nil, false
		}
		switch b[i] {
		case ',':
			i = skipWS(b, i+1)
		case '}':
			return m, skipWS(b, i+1) == len(b)
		default:
			return nil, false
		}
	}
}

func skipWS(b []byte, i int) int {
	for i < len(b) && (b[i] == ' ' || b[i] == '\t' || b[i] == '\n' || b[i] == '\r') {
		i++
	}
	return i
}

// scanString returns the index after the string opening at b[i] and whether
// it contained an escape; -1 when unterminated or holding a control byte.
func scanString(b []byte, i int) (int, bool) {
	escaped := false
	for j := i + 1; j < len(b); j++ {
		switch c := b[j]; {
		case c == '"':
			return j + 1, escaped
		case c == '\\':
			escaped = true
			j++
		case c < 0x20:
			return -1, false
		}
	}
	return -1, false
}

// scanValue returns the index after the value starting at b[i], or -1.
func scanValue(b []byte, i int) int {
	if i >= len(b) {
		return -1
	}
	switch b[i] {
	case '"':
		end, _ := scanString(b, i)
		return end
	case '{', '[':
		depth := 0
		for j := i; j < len(b); j++ {
			switch b[j] {
			case '"':
				end, _ := scanString(b, j)
				if end < 0 {
					return -1
				}
				j = end - 1
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return j + 1
				}
			}
		}
		return -1
	}
	j := i
	for j < len(b) {
		switch b[j] {
		case ',', '}', ']', ' ', '\t', '\n', '\r':
			if j == i {
				return -1
			}
			return scalarEnd(b, i, j)
		}
		j++
	}
	return scalarEnd(b, i, j)
}

// scalarEnd accepts a number or literal only when encoding/json does.
func scalarEnd(b []byte, i, j int) int {
	if !json.Valid(b[i:j]) {
		return -1
	}
	return j
}

// field decodes one top-level value with json.Number numbers. ok is false when
// the key is absent.
func (m topObject) field(key string) (v any, ok bool, err error) {
	raw, ok := m[key]
	if !ok {
		return nil, false, nil
	}
	err = decode(raw, &v)
	return v, true, err
}

// set re-encodes one top-level value.
func (m topObject) set(key string, v any) error {
	b, err := marshal(v)
	if err != nil {
		return err
	}
	m[key] = b
	return nil
}

// isArray reports a JSON array value (Gemini's contents); null is not one.
func (m topObject) isArray(key string) bool {
	raw := bytes.TrimLeft(m[key], " \t\r\n")
	return len(raw) > 0 && raw[0] == '['
}

// marshal writes the object with sorted keys, as encoding/json does, but
// copies each value's bytes as they are: re-compacting a megabyte of contents
// through the encoder's scanner was the slowest step of the whole adapter.
// Every value is valid JSON already (decoded by decodeTop or produced by set).
func (m topObject) marshal() ([]byte, error) {
	keys := make([]string, 0, len(m))
	size := 2
	for k, v := range m {
		keys = append(keys, k)
		size += len(k) + len(v) + 4
	}
	sort.Strings(keys)
	out := make([]byte, 0, size+16)
	out = append(out, '{')
	for i, k := range keys {
		if i > 0 {
			out = append(out, ',')
		}
		name, err := marshal(k)
		if err != nil {
			return nil, err
		}
		out = append(out, name...)
		out = append(out, ':')
		out = append(out, m[k]...)
	}
	return append(out, '}'), nil
}
