package apicontract

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed ml.json
var ml []byte

//go:embed public.json
var public []byte

var compiled sync.Map

func Validate(family, name string, value any) error {
	key := family + "/" + name
	schema, ok := compiled.Load(key)
	if !ok {
		data, fragment := ml, "#/$defs/"
		if family == "public" {
			data, fragment = public, "#/components/schemas/"
		} else if family != "ml" {
			return fmt.Errorf("unknown contract")
		}
		data = []byte(strings.ReplaceAll(string(data), "../ml/contracts/v1/contracts.schema.json", "urn:api:ml"))
		var document any
		if err := json.Unmarshal(data, &document); err != nil {
			return err
		}
		compiler := jsonschema.NewCompiler()
		compiler.AssertFormat()
		if family == "public" {
			var mlDoc any
			if err := json.Unmarshal(ml, &mlDoc); err != nil {
				return err
			}
			if err := compiler.AddResource("urn:api:ml", mlDoc); err != nil {
				return err
			}
		}
		if err := compiler.AddResource("urn:api:"+family, document); err != nil {
			return err
		}
		built, err := compiler.Compile("urn:api:" + family + fragment + name)
		if err != nil {
			return err
		}
		schema, _ = compiled.LoadOrStore(key, built)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return err
	}
	return schema.(*jsonschema.Schema).Validate(normalized)
}
