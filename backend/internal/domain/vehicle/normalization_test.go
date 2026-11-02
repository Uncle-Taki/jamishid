package vehicle

import (
	"encoding/json"
	"os"
	"testing"
)

func TestSharedNormalizationContract(t *testing.T) {
	raw, err := os.ReadFile("normalization_contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		Version  string
		Cases    []struct{ Input, Text, Model string }
		Distinct [][]string
	}
	if err = json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	for _, c := range contract.Cases {
		if got := NormalizeSearchText(c.Input); got != c.Text {
			t.Errorf("text %q: %q != %q", c.Input, got, c.Text)
		}
		if got := NormalizeModelText(c.Input); got != c.Model {
			t.Errorf("model %q: %q != %q", c.Input, got, c.Model)
		}
	}
	for _, pair := range contract.Distinct {
		if NormalizeModelText(pair[0]) == NormalizeModelText(pair[1]) {
			t.Errorf("merged distinct models: %v", pair)
		}
	}
}
