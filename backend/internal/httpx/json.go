package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
)

const maxBody = 1 << 20

// Validator is implemented by request bodies that validate themselves after decoding.
type Validator interface{ Validate() error }

// Decode reads a JSON body (unknown fields rejected) and runs Validate when available.
func Decode(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return BadRequest("istek gövdesi boş")
		}
		return BadRequest("geçersiz JSON: " + err.Error())
	}
	if val, ok := v.(Validator); ok {
		return val.Validate()
	}
	return nil
}

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Page is the keyset pagination envelope. NextCursor is empty on the last page.
type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// Limit parses ?limit= with bounds.
func Limit(r *http.Request, def, max int) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// Int64Param parses an int64 from s, returning a 400 error on failure.
func Int64Param(s, name string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, BadRequest(name + " geçersiz")
	}
	return n, nil
}
