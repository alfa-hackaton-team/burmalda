package apicontract

import (
	"encoding/json"
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestEmbeddedContractsMatchDocumentation(t *testing.T) {
	for _, item := range []struct {
		path     string
		embedded []byte
		yaml     bool
	}{{"../../../docs/ml/contracts/v1/contracts.schema.json", ml, false}, {"../../../docs/api/schemas.yaml", public, true}} {
		data, err := os.ReadFile(item.path)
		if os.IsNotExist(err) {
			t.Skip("repository documentation is not mounted")
		}
		if err != nil {
			t.Fatal(err)
		}
		var expected, actual any
		if item.yaml {
			if err = yaml.Unmarshal(data, &expected); err != nil {
				t.Fatal(err)
			}
			expected.(map[string]any)["$schema"] = "https://json-schema.org/draft/2020-12/schema"
		} else if err = json.Unmarshal(data, &expected); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(item.embedded, &actual); err != nil {
			t.Fatal(err)
		}
		a, _ := json.Marshal(expected)
		b, _ := json.Marshal(actual)
		if string(a) != string(b) {
			t.Fatal("embedded contracts are stale; run generate.py")
		}
	}
}
