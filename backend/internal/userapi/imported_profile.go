package userapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// importedProfile preserves imported field values and their dataset provenance.
// It does not infer revenue periods, liquidity, financing needs or payment behavior.
func importedProfile(companyID string, data object) (object, error) {
	company, err := uuid.Parse(companyID)
	if err != nil || str(data["bundle_id"]) == "" || data["row_no"] == nil {
		return nil, fmt.Errorf("imported company provenance is missing")
	}
	bundle := str(data["bundle_id"])
	sum := sha256.Sum256([]byte(bundle + "\x00" + "1"))
	snapshot := "dataset:" + hex.EncodeToString(sum[:])
	keys := []string{"synthetic_u", "org_type", "mainokved_code", "mainokved_name", "region", "city",
		"company_age_years_gradation", "amount_workers_fns_gradation", "amount_workers_spark_gradation",
		"cl_product_rko_mark", "cl_product_gar_mark", "cl_product_loa_mark", "cl_product_lea_mark",
		"cl_product_dep_mark", "cl_product_zpp_mark", "fts_flag", "fts_country_name", "factoring_mark"}
	sort.Strings(keys)
	features := []any{}
	profile := object{}
	for _, key := range keys {
		value := data[key]
		if value == nil {
			continue
		}
		if text, ok := value.(string); ok && strings.TrimSpace(text) == "" {
			continue
		}
		profile[key] = value
		feature := object{"id": uuid.NewSHA1(company, []byte(snapshot+"/"+key)).String(),
			"feature_key": key, "feature_value": value, "source_type": "company_field",
			"source_ref": object{"field": "dataset_company_profiles." + key, "source_snapshot_id": snapshot,
				"record_refs":        []string{fmt.Sprintf("company_profiles:%v", data["row_no"])},
				"derivation_version": "imported-fields-v1", "contributing_company_ids": []string{companyID}}}
		if err := checked("ml", "ProfileFeature", feature); err != nil {
			return nil, err
		}
		features = append(features, feature)
	}
	profile["profile_features"] = features
	return profile, nil
}
