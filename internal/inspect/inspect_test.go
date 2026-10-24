package inspect

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/traP-jp/isutori/internal/userscript"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRecording(t *testing.T) {
	s, err := New(t.TempDir(), "https://portal.example/contestant", "missing.user.js")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.identities["page"] = userscript.Verification{Actual: "sha256:test"}
	handler := s.Handler()
	send := func(event Event, token string) *httptest.ResponseRecorder {
		b, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "/api/inspect", bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	event := Event{Page: "page", ScriptID: "sha256:test", Sequence: 1, At: time.Now(), URL: "https://portal.example/contestant", Reason: "start", Visibility: "visible", HTML: "<body>loading</body>"}
	if w := send(event, "wrong"); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized: %d", w.Code)
	}
	bad := event
	bad.URL = "https://other.example/"
	if w := send(bad, s.Token); w.Code != 400 {
		t.Fatalf("wrong origin: %d", w.Code)
	}
	bad = event
	bad.Snapshot = "../../escape"
	if w := send(bad, s.Token); w.Code != 400 {
		t.Fatalf("client filename: %d", w.Code)
	}
	bad = event
	bad.URL += "?token=secret"
	if w := send(bad, s.Token); w.Code != 400 {
		t.Fatalf("query captured: %d", w.Code)
	}
	if w := send(event, s.Token); w.Code != 200 {
		t.Fatalf("first: %d %s", w.Code, w.Body.String())
	}
	event.Sequence++
	event.Reason = "tick"
	event.HTML = ""
	if w := send(event, s.Token); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	event.Sequence++
	event.Reason = "mutation"
	event.HTML = "<body>loaded</body>"
	if w := send(event, s.Token); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	b, err := os.ReadFile(filepath.Join(s.Dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines: %s", b)
	}
	for i, line := range lines {
		var saved Event
		if err := json.Unmarshal([]byte(line), &saved); err != nil {
			t.Fatal(err)
		}
		if saved.Sequence != i+1 || saved.ReceivedAt.IsZero() || saved.HTML != "" {
			t.Fatalf("bad event: %+v", saved)
		}
		html, err := os.ReadFile(filepath.Join(s.Dir, saved.Snapshot))
		if err != nil {
			t.Fatal(err)
		}
		expected := "<body>loading</body>"
		if i == 2 {
			expected = "<body>loaded</body>"
		}
		if string(html) != expected {
			t.Fatalf("snapshot %d: %s", i, html)
		}
	}
	s.bytes = maxSessionBytes
	if w := send(event, s.Token); w.Code != 500 {
		t.Fatalf("capacity not enforced: %d", w.Code)
	}
	s.bytes = 0
	s.count = 10000
	if w := send(event, s.Token); w.Code != 500 {
		t.Fatalf("event limit not enforced: %d", w.Code)
	}
}

func TestFailedSaveIsNotAcknowledged(t *testing.T) {
	s, err := New(t.TempDir(), "https://portal.example", "missing.user.js")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.identities["page"] = userscript.Verification{Actual: "sha256:test"}
	path := filepath.Join(s.Dir, "snapshots")
	if err := os.Rename(path, path+"-moved"); err != nil {
		t.Fatal(err)
	}
	event := Event{Page: "page", ScriptID: "sha256:test", Sequence: 1, At: time.Now(), URL: "https://portal.example/", Reason: "start", Visibility: "visible", HTML: "<body/>"}
	b, _ := json.Marshal(event)
	r := httptest.NewRequest("POST", "/api/inspect", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+s.Token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 500 {
		t.Fatalf("acknowledged failed save: %d", w.Code)
	}
	data, _ := os.ReadFile(filepath.Join(s.Dir, "events.jsonl"))
	if len(data) != 0 {
		t.Fatalf("referenced missing snapshot: %s", data)
	}
}

func TestLimitsAndAuthentication(t *testing.T) {
	s, err := New(t.TempDir(), "https://portal.example", "missing.user.js")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.identities["page"] = userscript.Verification{Actual: "sha256:test"}
	for _, body := range []string{`{} {}`, `{"html":"` + strings.Repeat("x", maxEventBytes+1) + `"}`} {
		r := httptest.NewRequest("POST", "/api/inspect", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+s.Token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code == 200 {
			t.Fatal("accepted invalid body")
		}
	}
	r := httptest.NewRequest("POST", "/api/inspect/start", strings.NewReader(`{"page":"handshake","source":"function isutoriScript() { return 1; }"}`))
	r.Header.Set("Authorization", "Bearer "+s.Token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), s.Origin) {
		t.Fatalf("handshake: %d %s", w.Code, w.Body)
	}
	if _, err := Listen("0.0.0.0:0"); err == nil {
		t.Fatal("accepted public listener")
	}
	for _, portal := range []string{"file:///etc/passwd", "https://user:pass@portal.example", "not a url"} {
		if s, err := New(t.TempDir(), portal, "missing.user.js"); err == nil {
			s.Close()
			t.Fatalf("accepted %s", portal)
		}
	}
}

func TestServeStops(t *testing.T) {
	ln, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(t.TempDir(), "https://portal.example", "missing.user.js")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.identities["page"] = userscript.Verification{Actual: "sha256:test"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, ln, s) }()
	req, _ := http.NewRequest("GET", "http://"+ln.Addr().String()+"/api/inspect", nil)
	req.Header.Set("Authorization", "Bearer "+s.Token)
	client := &http.Client{Timeout: time.Second}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("receiver did not stop")
	}
}

func TestConcurrentEvents(t *testing.T) {
	s, err := New(t.TempDir(), "https://portal.example", "missing.user.js")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.identities["page"] = userscript.Verification{Actual: "sha256:test"}
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.append(&Event{Page: "page", ScriptID: "sha256:test", Sequence: i + 1, At: time.Now(), URL: s.Origin + "/", Reason: "mutation", Visibility: "visible", HTML: "<body>state</body>"})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	b, err := os.ReadFile(filepath.Join(s.Dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 12 {
		t.Fatalf("lost events: %d", len(lines))
	}
	for _, line := range lines {
		var saved Event
		if err := json.Unmarshal([]byte(line), &saved); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(s.Dir, saved.Snapshot)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInspectionIdentity(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "script.user.js")
	script, err := userscript.Render(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := userscript.FileSource(script)
	if err != nil {
		t.Fatal(err)
	}
	id, err := userscript.SourceID(source)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(root, "https://portal.example", path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := s.Handler()
	post := func(route string, v any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(v)
		r := httptest.NewRequest("POST", route, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+s.Token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	event := Event{Page: "actual-page", ScriptID: id, Sequence: 1, At: time.Now(), URL: s.Origin + "/", Reason: "start", Visibility: "visible", HTML: "<body>state</body>"}
	if w := post("/api/inspect", event); w.Code == 200 {
		t.Fatal("accepted unregistered source")
	}
	w := post("/api/inspect/start", map[string]string{"page": "actual-page", "source": source})
	var info struct {
		Userscript userscript.Verification `json:"userscript"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil || info.Userscript.Status != "matched" {
		t.Fatalf("handshake: %d %s %v", w.Code, w.Body, err)
	}
	for _, status := range []userscript.Status{"matched", "mismatched", "unknown"} {
		if status == "mismatched" {
			if err := os.WriteFile(path, []byte(strings.Replace(script, "async prepare(target)", "async prepare(target /* changed */)", 1)), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if status == "unknown" {
			if err := os.WriteFile(path, []byte("partial edit"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		w := post("/api/inspect", event)
		var v userscript.Verification
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil || v.Status != status || v.Actual != id {
			t.Fatalf("reference change: %d %s %v", w.Code, w.Body, err)
		}
		event.Sequence++
	}
	event.Userscript = userscript.Verification{Status: "matched"}
	if w := post("/api/inspect", event); w.Code != 400 {
		t.Fatal("trusted a client-supplied verification")
	}
	if w := post("/api/inspect/start", map[string]string{"page": "actual-page", "source": "function isutoriScript() { return 1; }"}); w.Code != 409 {
		t.Fatal("changed an existing page identity")
	}
}

func TestInspectionWithoutSource(t *testing.T) {
	root := t.TempDir()
	script, err := userscript.Render(nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "script.user.js")
	if err := os.WriteFile(path, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := New(root, "https://portal.example", path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := s.Handler()
	post := func(route string, value any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(value)
		r := httptest.NewRequest("POST", route, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+s.Token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	start := map[string]string{"page": "unknown-page", "source": "function isutoriScript() { [native code] }"}
	w := post("/api/inspect/start", start)
	var info struct {
		Userscript userscript.Verification `json:"userscript"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &info); w.Code != 200 || err != nil || info.Userscript.Status != "unknown" || info.Userscript.Actual != "" || info.Userscript.Expected == "" || info.Userscript.Error == "" {
		t.Fatalf("handshake: %d %s %v", w.Code, w.Body, err)
	}
	event := Event{Page: "unregistered-page", Sequence: 1, At: time.Now(), URL: s.Origin + "/", Reason: "start", Visibility: "visible", HTML: "<body>state</body>"}
	if w := post("/api/inspect", event); w.Code == 200 {
		t.Fatal("accepted unregistered page with empty identity")
	}
	event.Page = "unknown-page"
	for i := 0; i < 2; i++ {
		w := post("/api/inspect", event)
		var v userscript.Verification
		if err := json.Unmarshal(w.Body.Bytes(), &v); w.Code != 200 || err != nil || v.Status != "unknown" || v.Actual != "" || v.Error != info.Userscript.Error {
			t.Fatalf("event: %d %s %v", w.Code, w.Body, err)
		}
		event.Sequence++
		event.HTML = ""
	}
	b, err := os.ReadFile(filepath.Join(s.Dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(b), `"status":"unknown"`) != 2 {
		t.Fatalf("missing unknown recordings: %s", b)
	}
	if w := post("/api/inspect/start", start); w.Code != 200 {
		t.Fatalf("retry: %d %s", w.Code, w.Body)
	}
	start["source"] = "function isutoriScript() {}"
	if w := post("/api/inspect/start", start); w.Code != 409 {
		t.Fatal("changed the registered identity")
	}
}
