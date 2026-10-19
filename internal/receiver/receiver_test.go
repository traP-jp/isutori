package receiver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandler(t *testing.T) {
	results := make(chan Result, 1)
	srv := httptest.NewServer(handler("run1", "", results))
	defer srv.Close()

	post := func(contentType, body string) int {
		res, err := http.Post(srv.URL+"/api/result", contentType, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	if got := post("text/plain", `{"run_id":"run1"}`); got != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain: got %d", got)
	}
	if got := post("application/json", `{"run_id":"other"}`); got != http.StatusConflict {
		t.Errorf("wrong run_id: got %d", got)
	}
	if got := post("application/json", `{"run_id":"run1","passed":true,"score":42}`); got != http.StatusNoContent {
		t.Errorf("valid result: got %d", got)
	}
	if got := post("application/json", `{"run_id":"run1"}`); got != http.StatusConflict {
		t.Errorf("second result: got %d", got)
	}
	if r := <-results; !r.Passed || r.Score != 42 {
		t.Errorf("got %+v", r)
	}

	res, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("manual page: got %d", res.StatusCode)
	}
}
