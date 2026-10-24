// Package strictjson decodes exactly one JSON object with no unknown fields.
package strictjson

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
)

func Decode(r io.Reader, v any) error {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return errors.New("expected a single JSON object")
	}
	return nil
}

// DecodeRequest decodes the body of r into v, or answers with an error and
// returns false. Requiring application/json forces a CORS preflight, which
// these loopback servers never answer, so other pages cannot post to them;
// userscripts bypass CORS.
func DecodeRequest(w http.ResponseWriter, r *http.Request, maxBytes int64, v any) bool {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return false
	}
	if err := Decode(http.MaxBytesReader(w, r.Body, maxBytes), v); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}
