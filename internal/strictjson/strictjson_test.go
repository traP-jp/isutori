package strictjson

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecode(t *testing.T) {
	var v struct {
		A int `json:"a"`
	}
	for body, ok := range map[string]bool{
		`{"a":1}`:         true,
		`{"a":1} `:        true,
		`{"a":1,"b":2}`:   false,
		`{"a":1}{"a":2}`:  false,
		`{"a":1} garbage`: false,
		``:                false,
	} {
		if err := Decode(strings.NewReader(body), &v); (err == nil) != ok {
			t.Errorf("Decode(%q): %v", body, err)
		}
	}
}

func TestDecodeRequest(t *testing.T) {
	for _, tc := range []struct {
		contentType, body string
		code              int
	}{
		{"application/json; charset=utf-8", `{}`, http.StatusOK},
		{"text/plain", `{}`, http.StatusUnsupportedMediaType},
		{"application/json", `{"x":1}`, http.StatusBadRequest},
		{"application/json", `{"pad":"` + strings.Repeat("x", 64) + `"}`, http.StatusBadRequest},
	} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(tc.body))
		r.Header.Set("Content-Type", tc.contentType)
		w := httptest.NewRecorder()
		var v struct {
			Pad string `json:"pad"`
		}
		if DecodeRequest(w, r, 32, &v) {
			w.WriteHeader(http.StatusOK)
		}
		if w.Code != tc.code {
			t.Errorf("%s %s: got %d, want %d", tc.contentType, tc.body, w.Code, tc.code)
		}
	}
}
