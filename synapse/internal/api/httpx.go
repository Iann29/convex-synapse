package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

// writeJSON marshals v as JSON with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

// writeError emits a structured error JSON response.
//
// The shape mirrors the Convex Cloud error envelope: { code, message }. Any
// extra detail is logged server-side, never leaked to the caller, to avoid
// turning the API into an information-disclosure vector.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{
		"code":    code,
		"message": message,
	})
}

// readJSON decodes a JSON request body, rejecting unknown fields. The 1MB cap
// keeps a single request from chewing arbitrary memory.
func readJSON(r *http.Request, dst any) error {
	const maxBody = 1 << 20 // 1MB
	r.Body = http.MaxBytesReader(nil, r.Body, maxBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("request body must be a single JSON object")
	}
	return nil
}

// logErr is a tiny helper to log a server-side error without leaking it.
func logErr(msg string, err error) {
	slog.Default().Error(msg, "err", err)
}
