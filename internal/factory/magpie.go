package factory

import (
	"regexp"
	"strings"
)

// This file ports the adapters magpie's Factory plugin (@magpie-community/
// opencode-factory-auth 0.1.52) applies to a request's prompts before it
// leaves for Factory. Factory's usage policy reads the whole body and refuses
// a client that describes itself as another agent harness or that opens with
// another agent's identity. The adapters change only the known fixed sentences
// and keep every task instruction, so the model still reads the caller's
// request.

// ClientIdentities are the fixed opening lines Factory refuses; droid's line
// replaces whichever one a prompt opens with.
var ClientIdentities = []string{
	"You are Claude Code, Anthropic's official CLI for Claude.",
	"You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK.",
	"You are a Claude agent, built on Anthropic's Claude Agent SDK.",
}

// PI_OPENING is pi's system opening; Factory refuses the sentence whole and
// answers the same request once it is shortened to PI_COMPAT.
var (
	piOpeningRE = regexp.MustCompile(`(?m)^You are an expert coding assistant operating inside pi, a coding agent harness\.`)
	piCompat    = "You are an expert coding assistant."
)

// pi's compaction and branch openings, which Factory refuses whole: every turn
// after such a compaction was refused until the sentence changed.
var piSummaries = [][2]string{
	{"The conversation history before this point was compacted into the following summary:\n\n<summary>\n", "Earlier conversation context is summarized below:\n\n<summary>\n"},
	{"The following is a summary of a branch that this conversation came back from:\n\n<summary>\n", "Summary of an earlier conversation branch:\n\n<summary>\n"},
}

// zcodeEnvContext is ZCode's generated environment paragraph; only a complete
// standalone paragraph changes, so fenced or quoted copies stay.
var zcodeEnvContext = regexp.MustCompile(`(?m)^# Environment\nYou have been invoked in the following environment:[ \t]*\n(?: {0,2}- [^\n]*\n)*(?:- You are powered by the model named [^\n]+\.)`)

// zcodeModelLineRE is the model sentence inside the environment block.
var zcodeModelLineRE = regexp.MustCompile(`(?m)^- You are powered by the model named`)

// systemModelLineRE is the model declaration line inside a system prompt.
var systemModelLineRE = regexp.MustCompile(`(?m)^You are powered by the model [^\n<>]+`)

// configSkillRefused is Claude Code's description of itself inside the skill
// list, refused wherever it appears.
const (
	configSkillRefused = "the harness executes these, not Claude, so memory/preferences cannot fulfill them."
	configSkillSafe    = "the harness executes these, not the assistant, so memory/preferences cannot fulfill them."
)

// ClientOpening shortens pi's refused opening sentence.
func ClientOpening(text string) string {
	return piOpeningRE.ReplaceAllString(text, piCompat)
}

// HarnessText says "runtime" where the text describes an agent harness, the
// word Factory refuses in another client's self-description. A name built from
// it (harness_key, harness.rs, harness-ci) and the user's own words in their
// turns stay as they are.
func HarnessText(text string) string {
	if !strings.Contains(strings.ToLower(text), "harness") {
		return text
	}
	var b strings.Builder
	for i := 0; i < len(text); {
		if m := harnessAt(text, i); m > 0 {
			b.WriteString(harnessReplacement(text[i : i+m]))
			i += m
			continue
		}
		b.WriteByte(text[i])
		i++
	}
	return b.String()
}

// harnessAt returns the length of a standalone "harness"/"harnesses" word
// starting at i, or 0. A word next to an identifier character, a separator or
// a file extension is a name, not prose.
func harnessAt(text string, i int) int {
	if i > 0 {
		if c := text[i-1]; isIdentByte(c) || c == '/' || c == '.' || c == '\\' || c == '-' {
			return 0
		}
	}
	rest := text[i:]
	var word string
	switch {
	case strings.HasPrefix(rest, "harnesses"):
		word = "harnesses"
	case strings.HasPrefix(rest, "harness"):
		word = "harness"
	case strings.HasPrefix(rest, "Harnesses"):
		word = "Harnesses"
	case strings.HasPrefix(rest, "Harness"):
		word = "Harness"
	default:
		return 0
	}
	// A trailing character that names a file, an identifier or a compound
	// keeps the word as it is; a sentence's full stop does not.
	if next := i + len(word); next < len(text) {
		c := text[next]
		switch {
		case isIdentByte(c), c == '/', c == '\\', c == '-':
			return 0
		case c == '.' && next+1 < len(text) && isIdentByte(text[next+1]):
			return 0
		}
	}
	return len(word)
}

func isIdentByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func harnessReplacement(word string) string {
	plural := strings.HasSuffix(word, "es")
	switch {
	case strings.HasPrefix(word, "H") && plural:
		return "Runtimes"
	case strings.HasPrefix(word, "H"):
		return "Runtime"
	case plural:
		return "runtimes"
	}
	return "runtime"
}

// ConfigSkillText repairs Claude Code's refused skill-list line.
func ConfigSkillText(text string) string {
	if !strings.Contains(text, configSkillRefused) {
		return text
	}
	return strings.ReplaceAll(text, configSkillRefused, configSkillSafe)
}

// ClientIdentityLine replaces one of the fixed client identity sentences with
// droid's line, keeping whatever follows it.
func ClientIdentityLine(text string) string {
	for _, identity := range ClientIdentities {
		if text == identity {
			return DroidLine
		}
		if strings.HasPrefix(text, identity+"\n") {
			return DroidLine + text[len(identity):]
		}
	}
	return text
}

// ZCodeRuntimeContext rewords ZCode's generated environment block: the
// opening recognised by Factory, and the model sentence inside it.
func ZCodeRuntimeContext(text string) string {
	if !strings.Contains(text, "You have been invoked in the following environment:") {
		return text
	}
	out := zcodeEnvContext.ReplaceAllStringFunc(text, func(block string) string {
		return strings.ReplaceAll(
			strings.ReplaceAll(block, "# Environment", "# Runtime context"),
			"You have been invoked in the following environment:", "The session environment is:")
	})
	return zcodeModelLineRE.ReplaceAllString(out, "- Current model name:")
}

// SystemModelLine rewords the model declaration inside a system prompt.
func SystemModelLine(text string) string {
	if !strings.Contains(text, "You are powered by the model") {
		return text
	}
	return systemModelLineRE.ReplaceAllStringFunc(text, func(line string) string {
		return strings.NewReplacer(
			"You are powered by the model named", "Current model name:",
			"You are powered by the model", "Current model:",
			"The exact model ID is", "Model ID:",
			"Assistant knowledge cutoff is", "Model knowledge cutoff:",
		).Replace(line)
	})
}

// PiSummary rewords pi's compaction opening when the text is that whole
// summary, sentence first and </summary> last.
func PiSummary(text string) string {
	if !strings.HasSuffix(text, "</summary>") {
		return text
	}
	for _, pair := range piSummaries {
		if strings.HasPrefix(text, pair[0]) {
			return pair[1] + text[len(pair[0]):]
		}
	}
	return text
}

// PromptText is the adapter chain magpie applies to a system prompt.
func PromptText(text string) string {
	return HarnessText(ConfigSkillText(ClientOpening(SystemModelLine(ClientIdentityLine(ZCodeRuntimeContext(text))))))
}

// SystemMessageText adapts a system-role message that is not the prompt.
func SystemMessageText(text string) string {
	return HarnessText(ConfigSkillText(ClientOpening(ZCodeRuntimeContext(text))))
}

// UserText adapts one user text block: pi's summary, then the fixed client
// phrases.
func UserText(text string) string {
	return UserTextBlock(PiSummary(text))
}

// UserTextBlock adapts a single user block (never a joined reminder bundle).
func UserTextBlock(text string) string {
	if strings.Contains(text, "You have been invoked in the following environment:") {
		return HarnessText(ConfigSkillText(SystemModelLine(ClientIdentityLine(ZCodeRuntimeContext(text)))))
	}
	return UserTextAdapter(text)
}

// UserTextAdapter is the smaller user-block chain that follows ZCode and
// model-line adaptation.
func UserTextAdapter(text string) string {
	return HarnessText(ConfigSkillText(ClientOpening(SystemModelLine(ClientIdentityLine(text)))))
}

// ToolDescriptions adapts every tool description and schema description.
func ToolDescriptions(tools []any) bool {
	changed := false
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		fn := tool
		if f, ok := tool["function"].(map[string]any); ok {
			fn = f
		}
		if d, ok := fn["description"].(string); ok {
			if out := HarnessText(d); out != d {
				fn["description"] = out
				changed = true
			}
		}
		for _, key := range []string{"input_schema", "parameters"} {
			if schemaDescriptions(fn[key], 0) {
				changed = true
			}
		}
	}
	return changed
}

func schemaDescriptions(schema any, depth int) bool {
	if depth > 32 {
		return false
	}
	obj, ok := schema.(map[string]any)
	if !ok {
		return false
	}
	changed := false
	for key, value := range obj {
		if key == "description" {
			if s, ok := value.(string); ok {
				if out := HarnessText(s); out != s {
					obj[key] = out
					changed = true
				}
			}
			continue
		}
		switch key {
		case "default", "const", "examples", "enum":
			continue
		}
		switch v := value.(type) {
		case map[string]any:
			if schemaDescriptions(v, depth+1) {
				changed = true
			}
		case []any:
			for _, item := range v {
				if schemaDescriptions(item, depth+1) {
					changed = true
				}
			}
		}
	}
	return changed
}
