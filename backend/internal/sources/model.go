package sources

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var (
	ErrInvalid  = errors.New("invalid source configuration")
	ErrNotFound = errors.New("source not found")
	ErrConflict = errors.New("source configuration changed")
)

type Config struct {
	Name                   string          `json:"name"`
	SourceType             string          `json:"source_type"`
	SourceRole             string          `json:"source_role"`
	TrustLevel             string          `json:"trust_level"`
	AdapterType            string          `json:"adapter_type"`
	Endpoint               string          `json:"endpoint"`
	Enabled                bool            `json:"enabled"`
	PollingIntervalSeconds int             `json:"polling_interval_seconds"`
	CredentialRef          *string         `json:"credential_ref,omitempty"`
	AdapterConfig          json.RawMessage `json:"adapter_config,omitempty"`
}

type Source struct {
	SourceID      string     `json:"source_id"`
	ConfigVersion string     `json:"config_version"`
	Config        Config     `json:"config"`
	Health        string     `json:"health"`
	LastSyncAt    *time.Time `json:"last_sync_at"`
	LastSuccessAt *time.Time `json:"last_success_at"`
	LastError     *string    `json:"last_error,omitempty"`
}

type Page struct {
	Items      []Source `json:"items"`
	NextCursor *string  `json:"next_cursor"`
}

func ParseCreateJSON(body []byte) (Config, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return Config{}, fmt.Errorf("%w: expected a JSON object", ErrInvalid)
	}
	for _, required := range []string{"name", "source_type", "source_role", "trust_level", "adapter_type", "endpoint", "enabled", "polling_interval_seconds"} {
		if _, ok := fields[required]; !ok {
			return Config{}, fmt.Errorf("%w: missing %s", ErrInvalid, required)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	for name, raw := range fields {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return Config{}, fmt.Errorf("%w: %s cannot be null", ErrInvalid, name)
		}
	}
	return cfg, nil
}

func (s Source) Version() (int64, error) {
	return strconv.ParseInt(s.ConfigVersion, 10, 64)
}

// ApplyPatch accepts only the fields from SourceUpdate. It keeps false, null,
// and missing fields distinct so disabling and clearing credential_ref work.
func ApplyPatch(current Config, body []byte) (Config, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || len(fields) == 0 {
		return Config{}, fmt.Errorf("%w: expected a nonempty JSON object", ErrInvalid)
	}
	for name, raw := range fields {
		if strings.TrimSpace(string(raw)) == "null" && name != "credential_ref" {
			return Config{}, fmt.Errorf("%w: %s cannot be null", ErrInvalid, name)
		}
		switch name {
		case "name":
			if err := json.Unmarshal(raw, &current.Name); err != nil {
				return Config{}, invalidField(name, err)
			}
		case "source_type":
			if err := json.Unmarshal(raw, &current.SourceType); err != nil {
				return Config{}, invalidField(name, err)
			}
		case "source_role":
			if err := json.Unmarshal(raw, &current.SourceRole); err != nil {
				return Config{}, invalidField(name, err)
			}
		case "trust_level":
			if err := json.Unmarshal(raw, &current.TrustLevel); err != nil {
				return Config{}, invalidField(name, err)
			}
		case "adapter_type":
			if err := json.Unmarshal(raw, &current.AdapterType); err != nil {
				return Config{}, invalidField(name, err)
			}
		case "endpoint":
			if err := json.Unmarshal(raw, &current.Endpoint); err != nil {
				return Config{}, invalidField(name, err)
			}
		case "enabled":
			if err := json.Unmarshal(raw, &current.Enabled); err != nil {
				return Config{}, invalidField(name, err)
			}
		case "polling_interval_seconds":
			if err := json.Unmarshal(raw, &current.PollingIntervalSeconds); err != nil {
				return Config{}, invalidField(name, err)
			}
		case "credential_ref":
			if err := json.Unmarshal(raw, &current.CredentialRef); err != nil {
				return Config{}, invalidField(name, err)
			}
		case "adapter_config":
			var object map[string]json.RawMessage
			if err := json.Unmarshal(raw, &object); err != nil || object == nil {
				return Config{}, fmt.Errorf("%w: adapter_config must be an object", ErrInvalid)
			}
			current.AdapterConfig = raw
		default:
			return Config{}, fmt.Errorf("%w: unknown field %s", ErrInvalid, name)
		}
	}
	return current, nil
}

func invalidField(name string, err error) error {
	return fmt.Errorf("%w: %s: %v", ErrInvalid, name, err)
}
