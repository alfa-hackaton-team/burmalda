package sources

import (
	"context"
	"errors"
	"testing"
)

func TestSourceCategoriesMatchML(t *testing.T) {
	for _, kind := range []string{"news", "bank_analytics", "expert", "regulator", "financial_press"} {
		for _, trust := range []string{"primary", "trusted", "standard", "unknown", "reviewed"} {
			cfg := validConfig()
			cfg.SourceType = kind
			cfg.TrustLevel = trust
			if err := (Validator{}).validate(context.Background(), &cfg, false); err != nil {
				t.Fatalf("%s/%s rejected: %v", kind, trust, err)
			}
		}
	}
	for _, tc := range []struct{ kind, trust string }{{"custom", "reviewed"}, {"news", "highest"}, {"NEWS", "standard"}, {"news", " trusted "}} {
		cfg := validConfig()
		cfg.SourceType = tc.kind
		cfg.TrustLevel = tc.trust
		if err := (Validator{}).validate(context.Background(), &cfg, false); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted %s/%s: %v", tc.kind, tc.trust, err)
		}
	}
}
