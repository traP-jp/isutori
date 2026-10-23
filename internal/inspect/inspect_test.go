package inspect

import (
	"bytes"
	"context"
	"encoding/json"
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
	s, err := New(t.TempDir(), "https://portal.example/contestant")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
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
	event := Event{Page: "page", Sequence: 1, At: time.Now(), URL: "https://portal.example/contestant", Reason: "start", Visibility: "visible", HTML: "<body>loading</body>"}
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
	if w := send(event, s.Token); w.Code != 204 {
		t.Fatalf("first: %d %s", w.Code, w.Body.String())
	}
	event.Sequence++
	event.Reason = "tick"
	event.HTML = ""
	if w := send(event, s.Token); w.Code != 204 {
		t.Fatal(w.Body.String())
	}
	event.Sequence++
	event.Reason = "mutation"
	event.HTML = "<body>loaded</body>"
	if w := send(event, s.Token); w.Code != 204 {
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
	s, err := New(t.TempDir(), "https://portal.example")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	path := filepath.Join(s.Dir, "snapshots")
	if err := os.Rename(path, path+"-moved"); err != nil {
		t.Fatal(err)
	}
	event := Event{Page: "page", Sequence: 1, At: time.Now(), URL: "https://portal.example/", Reason: "start", Visibility: "visible", HTML: "<body/>"}
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
	s, err := New(t.TempDir(), "https://portal.example")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, body := range []string{`{} {}`, `{"html":"` + strings.Repeat("x", maxEventBytes+1) + `"}`} {
		r := httptest.NewRequest("POST", "/api/inspect", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+s.Token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code == 204 {
			t.Fatal("accepted invalid body")
		}
	}
	r := httptest.NewRequest("GET", "/api/inspect", nil)
	r.Header.Set("Authorization", "Bearer "+s.Token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), s.Origin) {
		t.Fatalf("handshake: %d %s", w.Code, w.Body)
	}
	if _, err := Listen("0.0.0.0:0"); err == nil {
		t.Fatal("accepted public listener")
	}
	for _, portal := range []string{"file:///etc/passwd", "https://user:pass@portal.example", "not a url"} {
		if s, err := New(t.TempDir(), portal); err == nil {
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
	s, err := New(t.TempDir(), "https://portal.example")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
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
	s, err := New(t.TempDir(), "https://portal.example")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.append(Event{Page: "page", Sequence: i + 1, At: time.Now(), URL: s.Origin + "/", Reason: "mutation", Visibility: "visible", HTML: "<body>state</body>"})
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
