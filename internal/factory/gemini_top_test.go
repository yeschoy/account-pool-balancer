package factory

import (
	"bytes"
	"encoding/json"
	"testing"
)

func sameTop(t testing.TB, b []byte) {
	t.Helper()
	var want map[string]json.RawMessage
	wantErr := json.Unmarshal(b, &want)
	got, err := decodeTop(b)
	if wantErr != nil || want == nil {
		if err == nil {
			t.Fatalf("decodeTop accepted %q, encoding/json: %v", b, wantErr)
		}
		return
	}
	if err != nil {
		t.Fatalf("decodeTop(%q): %v", b, err)
	}
	if len(got) != len(want) {
		t.Fatalf("keys %v vs %v", got, want)
	}
	for k, v := range want {
		if !bytes.Equal(got[k], v) {
			t.Fatalf("%q: %s vs %s", k, got[k], v)
		}
	}
	out, err := got.marshal()
	if err != nil || !json.Valid(out) {
		t.Fatalf("marshal %q: %s %v", b, out, err)
	}
	var a, c any
	_ = json.Unmarshal(out, &a)
	_ = json.Unmarshal(b, &c)
	ab, _ := json.Marshal(a)
	cb, _ := json.Marshal(c)
	if !bytes.Equal(ab, cb) {
		t.Fatalf("round trip changed value:\n%s\n%s", ab, cb)
	}
}

func TestDecodeTopMatchesEncodingJSON(t *testing.T) {
	for _, s := range []string{
		`{}`, ` { } `, `{"a":1}`, `{"a":-1.5e3,"b":true,"c":null,"d":"x\"}{]","e":[1,{"f":"]"}],"g":{"h":"\\"}}`,
		`{"contents":[{"role":"user","parts":[{"text":"a \u003c b"}]}],"model":"m"}`,
		`{"k\u0065y":1}`, `{"a":1,"a":2}`, `{"a":"é😀"}`, "{\n\t\"a\" : [ 1 , 2 ] \r\n}",
		`{"a":9007199254740993}`,
		// malformed: every one must be an error
		``, `null`, `[]`, `{`, `{"a"}`, `{"a":}`, `{"a":1,}`, `{"a":1}x`, `{"a":tru}`, `{"a":01}`, `{"a":"x`, "{\"a\":\"\x01\"}", `{"a":[1}`,
		`{"a":[1,]}`, `{"a":{"b":tru}}`, `{"a":"\q"}`, `{"a":[1}}`, `{"a":[01]}`,
	} {
		sameTop(t, []byte(s))
	}
}

func FuzzDecodeTop(f *testing.F) {
	for _, s := range []string{`{"a":1}`, `{"a":[{"b":"]"}],"c":"\\""}`, `{"x":{"y":[1,2,{"z":null}]}}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		sameTop(t, b)
	})
}
