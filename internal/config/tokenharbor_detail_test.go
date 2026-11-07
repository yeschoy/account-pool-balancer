package config

import (
	"encoding/json"
	"testing"
)

func TestTokenHarborDetailDefaultsAndPersistence(t *testing.T) {
	c := Default()
	if c.TokenHarbor.DetailLog || TokenHarborDefaults().DetailLog {
		t.Fatal("TokenHarbor details must default off")
	}
	for _, enabled := range []bool{false, true} {
		c.TokenHarbor.DetailLog = enabled
		b, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		var restored Config
		if err := json.Unmarshal(b, &restored); err != nil {
			t.Fatal(err)
		}
		if err := restored.Normalize(); err != nil {
			t.Fatal(err)
		}
		if restored.TokenHarbor.DetailLog != enabled || restored.Clone().TokenHarbor.DetailLog != enabled {
			t.Fatal("detail switch lost on persistence/clone")
		}
	}
	var legacy TokenHarbor
	if err := json.Unmarshal([]byte(`{"baseUrl":"https://tokenharbor.ai/v1"}`), &legacy); err != nil || legacy.DetailLog {
		t.Fatal("legacy configuration enabled details")
	}
}
