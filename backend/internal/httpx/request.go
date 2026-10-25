package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
)

// DecodeJSONObject accepts exactly one JSON object. Endpoint-specific
// validators remain responsible for required strings, nullability, enums and
// numeric ranges after decoding.
func DecodeJSONObject(w http.ResponseWriter, r *http.Request, destination any) bool {
	if !validJSONContentType(r.Header.Values("Content-Type")) {
		WriteError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Content-Type must be application/json", false)
		return false
	}

	decoder := json.NewDecoder(r.Body)
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		writeJSONDecodeError(w, err)
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSONDecodeError(w, err)
		return false
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' || hasDuplicateJSONKey(trimmed) {
		WriteError(w, http.StatusBadRequest, "VALIDATION_ERROR", "request body must be one JSON object", false)
		return false
	}

	objectDecoder := json.NewDecoder(bytes.NewReader(trimmed))
	objectDecoder.DisallowUnknownFields()
	if err := objectDecoder.Decode(destination); err != nil {
		WriteError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid JSON object", false)
		return false
	}
	return true
}

func validJSONContentType(values []string) bool {
	if len(values) != 1 {
		return false
	}
	mediaType, parameters, err := mime.ParseMediaType(values[0])
	if err != nil || mediaType != "application/json" {
		return false
	}
	for name, value := range parameters {
		if name != "charset" || !strings.EqualFold(value, "utf-8") {
			return false
		}
	}
	return true
}

func writeJSONDecodeError(w http.ResponseWriter, err error) {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		WriteError(w, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "request body is too large", false)
		return
	}
	WriteError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid JSON body", false)
}

func hasDuplicateJSONKey(data []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	duplicate, err := inspectJSONValue(decoder)
	return err != nil || duplicate
}

func inspectJSONValue(decoder *json.Decoder) (bool, error) {
	token, err := decoder.Token()
	if err != nil {
		return false, err
	}
	delimiter, structured := token.(json.Delim)
	if !structured {
		return false, nil
	}
	switch delimiter {
	case '{':
		keys := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return false, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return false, errors.New("JSON object key is not a string")
			}
			if _, exists := keys[key]; exists {
				return true, nil
			}
			keys[key] = struct{}{}
			duplicate, err := inspectJSONValue(decoder)
			if err != nil || duplicate {
				return duplicate, err
			}
		}
		_, err = decoder.Token()
		return false, err
	case '[':
		for decoder.More() {
			duplicate, err := inspectJSONValue(decoder)
			if err != nil || duplicate {
				return duplicate, err
			}
		}
		_, err = decoder.Token()
		return false, err
	default:
		return false, errors.New("invalid JSON delimiter")
	}
}
