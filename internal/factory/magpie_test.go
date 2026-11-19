package factory

import "testing"

// A sentence's full stop must not stop the rewrite: "DeepSeek Harness." is a
// self-description Factory refuses, while harness.rs and harness_key are names.
func TestHarnessTextSentenceEnd(t *testing.T) {
	cases := map[string]string{
		"You are an AI agent powered by DeepSeek Harness.": "You are an AI agent powered by DeepSeek Runtime.",
		"the harness runs tools.":                          "the runtime runs tools.",
		"edit harness.rs now":                              "edit harness.rs now",
		"set harness_key here":                             "set harness_key here",
		"Harness":                                          "Runtime",
		"The harnesses differ":                             "The runtimes differ",
	}
	for in, want := range cases {
		if got := HarnessText(in); got != want {
			t.Errorf("HarnessText(%q) = %q, want %q", in, got, want)
		}
	}
}
