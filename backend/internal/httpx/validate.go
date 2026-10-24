package httpx

import "net/http"

// V accumulates field validation errors.
type V struct{ errs map[string]string }

func (v *V) Check(ok bool, field, msg string) {
	if ok {
		return
	}
	if v.errs == nil {
		v.errs = map[string]string{}
	}
	if _, exists := v.errs[field]; !exists {
		v.errs[field] = msg
	}
}

// Err returns a 422 VALIDATION_ERROR describing every failed field, or nil.
func (v *V) Err() error {
	if len(v.errs) == 0 {
		return nil
	}
	return &Error{Status: http.StatusUnprocessableEntity, Code: "VALIDATION_ERROR",
		Message: "girdi doğrulaması başarısız", Details: v.errs}
}
