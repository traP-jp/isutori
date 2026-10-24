package receiver

import (
	"encoding/json"
	"errors"
	"github.com/traP-jp/isutori/internal/userscript"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestHandler(t *testing.T) {
	rec := newReceiver(Options{RunID: "run1", Log: io.Discard, Save: func(Result) error { return nil }})
	results := rec.results
	srv := httptest.NewServer(rec.handler())
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
	attempts := 0
	saved := false
	rec := newReceiver(Options{RunID: "run1", Log: io.Discard, Save: func(res Result) error {
		attempts++
		if res.Score != 42 {
			t.Fatalf("saving wrong result: %+v", res)
		}
		if attempts == 1 {
			return errors.New("disk full")
		}
		saved = true
		return nil
	}})
	results := rec.results
	h := rec.handler()
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
	saves := 0
	rec := newReceiver(Options{RunID: "run1", ScriptPath: "", Log: io.Discard, Save: func(Result) error { saves++; return nil }})
	results := rec.results
	h := rec.handler()
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

func TestUserscriptGate(t *testing.T) {
	source := "function isutoriScript() { return 1; }"
	path := filepath.Join(t.TempDir(), "script.user.js")
	if err := os.WriteFile(path, []byte("// ==/UserScript==\n("+source+")();"), 0600); err != nil {
		t.Fatal(err)
	}
	id, err := userscript.SourceID(source)
	if err != nil {
		t.Fatal(err)
	}
	var saved Result
	rec := newReceiver(Options{RunID: "run1", ScriptPath: path, Log: io.Discard, Save: func(r Result) error { saved = r; return nil }})
	h := rec.handler()
	post := func(path string, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	body := `{"run_id":"run1","passed":true,"score":42,"userscript":{"actual":"` + id + `","status":"forged"}}`
	if w := post("/api/result", body); w.Code != 409 {
		t.Fatal("accepted a result without handshake")
	}
	wrong, _ := json.Marshal(map[string]string{"run_id": "run1", "source": "function isutoriScript() { return 2; }"})
	w := post("/api/userscript", string(wrong))
	var v userscript.Verification
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil || v.Status != "mismatched" {
		t.Fatalf("wrong version: %s %v", w.Body, err)
	}
	if w := post("/api/result", body); w.Code != 409 {
		t.Fatal("mismatch approved the client")
	}
	if w := post("/api/result", `{"run_id":"run1","passed":true,"score":42}`); w.Code != 409 {
		t.Fatal("accepted legacy automatic result")
	}
	if err := os.WriteFile(path, []byte("reference modified after receiver started"), 0600); err != nil {
		t.Fatal(err)
	}
	good, _ := json.Marshal(map[string]string{"run_id": "run1", "source": source})
	w = post("/api/userscript", string(good))
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil || v.Status != "matched" {
		t.Fatalf("fixed reference changed: %s %v", w.Body, err)
	}
	if w := post("/api/result", body); w.Code != 204 {
		t.Fatalf("verified result refused: %d %s", w.Code, w.Body)
	}
	if saved.Userscript.Status != "matched" || saved.Userscript.Expected != id {
		t.Fatalf("trusted client verification: %+v", saved)
	}
}

func TestManualInputAfterMismatch(t *testing.T) {
	var saved Result
	rec := newReceiver(Options{RunID: "run1", ScriptPath: "missing.user.js", Log: io.Discard, Save: func(r Result) error { saved = r; return nil }})
	h := rec.handler()
	r := httptest.NewRequest("POST", "/api/manual-result", strings.NewReader(`{"run_id":"run1","passed":true,"score":42,"userscript":{"status":"matched"}}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 204 || saved.Userscript.Status != "not_checked" || saved.Userscript.Actual != "" {
		t.Fatalf("manual result: %d %+v", w.Code, saved)
	}
}
