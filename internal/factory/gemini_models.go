package factory

// GeminiModel is one entry of Google's ListModels response. Only the fields a
// client needs to pick a generate target are filled; capability claims are
// configuration, not this account's entitlement.
type GeminiModel struct {
	Name                       string   `json:"name"`
	DisplayName                string   `json:"displayName,omitempty"`
	Description                string   `json:"description,omitempty"`
	SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
}

// GeminiModelList renders a Google-styled ListModels response for the given
// model ids. Every id is served only through Factory's captured generate
// route, so each entry advertises generateContent (and streamGenerateContent)
// and nothing else: countTokens goes through the same gateway call, and other
// Google methods (embedContent, batchGenerateContent, predict, …) are not
// offered by this pool.
func GeminiModelList(ids []string) []GeminiModel {
	out := make([]GeminiModel, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		out = append(out, GeminiModel{
			Name:                       "models/" + id,
			DisplayName:                id,
			SupportedGenerationMethods: []string{"generateContent", "streamGenerateContent"},
		})
	}
	return out
}
