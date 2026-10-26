package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"reflect"
	"strings"
)

// DecodeJSONObject accepts exactly one JSON object. Endpoint-specific
// validators remain responsible for required strings, nullability, enums and
// numeric ranges after decoding.
type JSONRequestError struct {
	Status        int
	Code, Message string
}

func (e *JSONRequestError) Error() string { return e.Message }

func DecodeJSONObject(w http.ResponseWriter, r *http.Request, destination any) bool {
	if err := ReadJSONObject(r, destination); err != nil {
		WriteError(w, err.Status, err.Code, err.Message, false)
		return false
	}
	return true
}
func ReadJSONObject(r *http.Request, destination any) *JSONRequestError {
	invalid := func(message string) *JSONRequestError { return &JSONRequestError{400, "VALIDATION_ERROR", message} }
	decodeError := func(err error) *JSONRequestError {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			return &JSONRequestError{413, "PAYLOAD_TOO_LARGE", "request body is too large"}
		}
		return invalid("invalid JSON body")
	}
	if !validJSONContentType(r.Header.Values("Content-Type")) {
		return invalid("Content-Type must be application/json")
	}
	decoder := json.NewDecoder(r.Body)
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return decodeError(err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return decodeError(err)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' || !validJSONFields(trimmed, reflect.TypeOf(destination)) {
		return invalid("request body must be one JSON object")
	}
	objectDecoder := json.NewDecoder(bytes.NewReader(trimmed))
	objectDecoder.DisallowUnknownFields()
	if err := objectDecoder.Decode(destination); err != nil {
		return invalid("invalid JSON object")
	}
	return nil
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

func validJSONFields(data []byte, destination reflect.Type) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	invalid, err := inspectJSONValue(decoder, destination)
	return err == nil && !invalid
}

func inspectJSONValue(decoder *json.Decoder, destination reflect.Type) (bool, error) {
	for destination != nil && destination.Kind() == reflect.Pointer {
		destination = destination.Elem()
	}
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
		var fields map[string]reflect.Type
		if destination != nil && destination.Kind() == reflect.Struct {
			fields = exactJSONFields(destination)
		}
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
			var fieldType reflect.Type
			if fields != nil {
				var exists bool
				fieldType, exists = fields[key]
				if !exists {
					return true, nil
				}
			} else if destination != nil && destination.Kind() == reflect.Map {
				fieldType = destination.Elem()
			}
			duplicate, err := inspectJSONValue(decoder, fieldType)
			if err != nil || duplicate {
				return duplicate, err
			}
		}
		_, err = decoder.Token()
		return false, err
	case '[':
		var elementType reflect.Type
		if destination != nil && (destination.Kind() == reflect.Slice || destination.Kind() == reflect.Array) {
			elementType = destination.Elem()
		}
		for decoder.More() {
			duplicate, err := inspectJSONValue(decoder, elementType)
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

// Match encoding/json's embedded-field precedence, but never its case-insensitive
// fallback. Map keys remain unrestricted; map values and array elements still
// inherit the DTO type so nested structs receive the same exact-name check.
func exactJSONFields(destination reflect.Type) map[string]reflect.Type {
	type candidate struct {
		typeOf reflect.Type
		depth  int
		tagged bool
	}
	candidates := map[string][]candidate{}
	visiting := map[reflect.Type]bool{}
	var collect func(reflect.Type, int)
	collect = func(current reflect.Type, depth int) {
		if visiting[current] {
			return
		}
		visiting[current] = true
		defer delete(visiting, current)
		for index := 0; index < current.NumField(); index++ {
			field := current.Field(index)
			embeddedType := field.Type
			if embeddedType.Kind() == reflect.Pointer {
				embeddedType = embeddedType.Elem()
			}
			if field.PkgPath != "" && (!field.Anonymous || embeddedType.Kind() != reflect.Struct) {
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if field.Anonymous && name == "" && embeddedType.Kind() == reflect.Struct {
				collect(embeddedType, depth+1)
				continue
			}
			tagged := name != ""
			if name == "" {
				name = field.Name
			}
			candidates[name] = append(candidates[name], candidate{field.Type, depth, tagged})
		}
	}
	collect(destination, 0)
	fields := map[string]reflect.Type{}
	for name, options := range candidates {
		best := options[0]
		count := 1
		for _, option := range options[1:] {
			if option.depth < best.depth || (option.depth == best.depth && option.tagged && !best.tagged) {
				best, count = option, 1
			} else if option.depth == best.depth && option.tagged == best.tagged {
				count++
			}
		}
		if count == 1 {
			fields[name] = best.typeOf
		}
	}
	return fields
}
