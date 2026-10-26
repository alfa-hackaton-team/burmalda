package userapi

import (
	"reflect"
	"testing"

	"github.com/google/uuid"
)

func TestImportedProfileKeepsSourceValuesAndStableEvidence(t *testing.T) {
	company := uuid.NewString()
	input := object{"bundle_id": "bundle", "row_no": float64(7), "mainokved_name": "Перевозки",
		"city": "", "region": nil, "cl_product_loa_mark": float64(0), "revenue_company": 1000}
	first, err := importedProfile(company, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := importedProfile(company, input)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatal("profile and evidence identities must be stable")
	}
	if first["cl_product_loa_mark"] != float64(0) || first["revenue_company"] != nil || first["city"] != nil {
		t.Fatalf("changed source semantics: %v", first)
	}
	features := first["profile_features"].([]any)
	if len(features) != 2 {
		t.Fatalf("unexpected evidence fields: %v", features)
	}
	for _, value := range features {
		feature := value.(object)
		if err := checked("ml", "ProfileFeature", feature); err != nil {
			t.Fatal(err)
		}
		ref := feature["source_ref"].(object)
		if ref["source_snapshot_id"] == "" || len(ref["record_refs"].([]string)) != 1 {
			t.Fatal("missing import provenance")
		}
	}
}

func TestImportedProfileRequiresDatasetProvenance(t *testing.T) {
	if _, err := importedProfile(uuid.NewString(), object{"mainokved_name": "Перевозки"}); err == nil {
		t.Fatal("accepted a profile without a source snapshot")
	}
}
