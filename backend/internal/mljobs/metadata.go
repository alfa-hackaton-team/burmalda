package mljobs

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/sources"
)

func deliveryMetadata(job Job) (json.RawMessage, error) {
	if err := sources.ValidateCategories(job.SourceType, job.TrustLevel); err != nil {
		return nil, fmt.Errorf("ML source categories are unsupported; verify the saved source snapshot")
	}
	var snapshot struct {
		Origin string   `json:"text_origin"`
		Issues []string `json:"extraction_issues"`
	}
	if err := json.Unmarshal(job.DeliveryExtraction, &snapshot); err != nil {
		return nil, fmt.Errorf("invalid saved extraction snapshot")
	}
	switch snapshot.Origin {
	case "full_article", "rss_summary", "pdf_extracted":
	default:
		return nil, fmt.Errorf("text origin is unknown; verify extraction provenance before ML delivery")
	}
	codes := []string{}
	for _, issue := range snapshot.Issues {
		code := issue
		switch issue {
		case "TEXT_TRUNCATED", "MISSING_PAGES", "MISSING_TABLES", "MISSING_FOOTNOTES", "EXTRACTION_FAILED", "LAYOUT_LOSS", "READING_ORDER_UNCERTAIN":
		default:
			var textPages, pdfPages int
			n, err := fmt.Sscanf(issue, "PAGE_COUNT_MISMATCH: TXT contains %d pages; PDF contains %d", &textPages, &pdfPages)
			if err != nil || n != 2 || textPages < 1 || pdfPages <= textPages || strings.TrimSpace(issue) != fmt.Sprintf("PAGE_COUNT_MISMATCH: TXT contains %d pages; PDF contains %d", textPages, pdfPages) {
				return nil, fmt.Errorf("unmapped extraction issue; verify provenance before ML delivery")
			}
			code = "MISSING_PAGES"
		}
		if !slices.Contains(codes, code) {
			codes = append(codes, code)
		}
	}
	slices.Sort(codes)
	return json.Marshal(struct {
		Method string   `json:"extraction_method"`
		Errors []string `json:"extraction_errors"`
	}{snapshot.Origin, codes})
}
