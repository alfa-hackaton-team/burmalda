package sources

import "fmt"

func ValidateCategories(sourceType, trustLevel string) error {
	switch sourceType {
	case "news", "bank_analytics", "expert", "regulator", "financial_press":
	default:
		return fmt.Errorf("%w: unsupported source_type", ErrInvalid)
	}
	switch trustLevel {
	case "primary", "trusted", "standard", "unknown", "reviewed":
	default:
		return fmt.Errorf("%w: unsupported trust_level", ErrInvalid)
	}
	return nil
}
