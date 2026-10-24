package httpx

import (
	"encoding/json"
	"net/http"
)

type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	TraceID   string `json:"trace_id,omitempty"`
	Retryable bool   `json:"retryable"`
}

func WriteJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}

func WriteError(w http.ResponseWriter, status int, code, message string, retryable bool) {
	WriteJSON(w, status, Error{
		Code:      code,
		Message:   message,
		Retryable: retryable,
	})
}
