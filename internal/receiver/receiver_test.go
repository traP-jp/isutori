package receiver

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestHandler(t *testing.T) {
	results := make(chan Result, 1)
	srv := httptest.NewServer(handler("run1", results, func(Result) error { return nil }))
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

func TestSaveBeforeAcknowledgement(t *testing.T) {
	results := make(chan Result, 1)
	attempts := 0
	saved := false
	h := handler("run1", results, func(res Result) error {
		attempts++
		if res.Score != 42 {
			t.Fatalf("saving wrong result: %+v", res)
		}
		if attempts == 1 {
			return errors.New("disk full")
		}
		saved = true
		return nil
	})
	post := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/result", strings.NewReader(`{"run_id":"run1","passed":true,"score":42}`))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := post(); w.Code != http.StatusInternalServerError || len(results) != 0 {
		t.Fatalf("acknowledged an unsaved result: %d, queued=%d", w.Code, len(results))
	}
	if w := post(); w.Code != http.StatusNoContent || !saved {
		t.Fatalf("retry did not save the result: %d", w.Code)
	}
	<-results
	if w := post(); w.Code != http.StatusConflict || attempts != 2 {
		t.Fatalf("accepted a duplicate after the result was consumed: %d, attempts=%d", w.Code, attempts)
	}
}

func TestConcurrentResultDelivery(t *testing.T) {
	results := make(chan Result, 1)
	saves := 0
	h := handler("run1", results, func(Result) error { saves++; return nil })
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest("POST", "/api/result", strings.NewReader(`{"run_id":"run1","passed":true,"score":42}`))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code == http.StatusNoContent {
				accepted.Add(1)
			} else if w.Code != http.StatusConflict {
				t.Errorf("unexpected response: %d", w.Code)
			}
		}()
	}
	wg.Wait()
	if saves != 1 || accepted.Load() != 1 || len(results) != 1 {
		t.Fatalf("saves=%d accepted=%d queued=%d", saves, accepted.Load(), len(results))
	}
}
