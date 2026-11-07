package quota

import (
	"context"
	"net/http"

	"account-pool-balancer/internal/opencode"
)

// Mistral base_model_id describes a model's ancestry. In particular a
// fine-tuned model must never be silently replaced with its base model.
func FetchMistralCatalog(ctx context.Context, hc *http.Client, base, key, seed string) (*Catalog, error) {
	cat, err := FetchCatalogFull(ctx, opencode.NoRedirect(hc), base, key, seed)
	if err != nil {
		return nil, err
	}
	for id, m := range cat.Models {
		m.Base = ""
		cat.Models[id] = m
	}
	return cat, nil
}
