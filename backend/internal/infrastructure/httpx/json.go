// Package httpx holds shared HTTP helpers: JSON I/O, error envelope and middleware.
package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
)

// ErrorBody is the API error envelope. The frontend translates by code; message is for logs.
type ErrorBody struct {
	Error struct {
		Code    string `json:"code"`
		Field   string `json:"field,omitempty"`
		Message string `json:"message"`
	} `json:"error"`
}

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		if err := json.NewEncoder(w).Encode(v); err != nil {
			slog.Error("encode response", "err", err)
		}
	}
}

func Error(w http.ResponseWriter, status int, code, msg string) {
	var b ErrorBody
	b.Error.Code, b.Error.Message = code, msg
	JSON(w, status, b)
}

// FieldError reports a problem with one input field, e.g. code "amount_required", field "amount".
func FieldError(w http.ResponseWriter, status int, code, field, msg string) {
	var b ErrorBody
	b.Error.Code, b.Error.Field, b.Error.Message = code, field, msg
	JSON(w, status, b)
}

// NotImplemented answers contract stubs so the frontend can see the route in Swagger.
func NotImplemented(w http.ResponseWriter, _ *http.Request) {
	Error(w, http.StatusNotImplemented, "not_implemented", "this endpoint is in the contract but not implemented yet")
}

func InternalError(w http.ResponseWriter, r *http.Request, err error) {
	slog.ErrorContext(r.Context(), "internal error", "err", err, "path", r.URL.Path, "request_id", RequestID(r.Context()))
	Error(w, http.StatusInternalServerError, "internal", "internal server error")
}

// Decode reads a JSON body, rejecting unknown fields and trailing data.
func Decode(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("body must contain a single JSON object")
	}
	return nil
}

// Page parses limit/offset query params with sane bounds.
func Page(r *http.Request, defLimit, maxLimit int) (limit, offset int) {
	limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ = strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 {
		limit = defLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}
