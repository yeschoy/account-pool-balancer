package opencode

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// decodeJSON retains number tokens in tool schemas and arguments. A protocol
// conversion must never turn an integer beyond 2^53 into a different argument.
func decodeJSON(b []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		if err != nil {
			return err
		}
		return errors.New("multiple JSON values")
	}
	return nil
}

func numeric(v any) bool {
	switch v.(type) {
	case float64, json.Number:
		return true
	}
	return false
}
