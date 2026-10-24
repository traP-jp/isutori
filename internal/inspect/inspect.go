package inspect

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/traP-jp/isutori/internal/strictjson"
	"github.com/traP-jp/isutori/internal/userscript"
)

const (
	maxStartBytes   = 1 << 20
	maxEventBytes   = 2 << 20
	maxSessionBytes = 100 << 20
	maxEvents       = 10000
	maxPages        = 10000
	maxPageIDLen    = 128
	maxReasonLen    = 100
	maxTargetLen    = 4096
)

type Event struct {
	Page       string                  `json:"page"`
	Sequence   int                     `json:"sequence"`
	At         time.Time               `json:"at"`
	URL        string                  `json:"url"`
	Reason     string                  `json:"reason"`
	Target     string                  `json:"target,omitempty"`
	Visibility string                  `json:"visibility"`
	HTML       string                  `json:"html,omitempty"`
	Snapshot   string                  `json:"snapshot,omitempty"`
	ReceivedAt time.Time               `json:"received_at"`
	ScriptID   string                  `json:"script_id"`
	Userscript userscript.Verification `json:"userscript,omitzero"`
}

type Session struct {
	Dir        string
	Token      string
	Origin     string
	ScriptPath string
	identities map[string]userscript.Verification
	mu         sync.Mutex
	events     *os.File
	count      int
	bytes      int64
	snapshots  map[string]string
}

func New(root, portal, scriptPath string) (*Session, error) {
	u, err := url.Parse(portal)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, errors.New("portal must be an http(s) URL without credentials")
	}
	dir := filepath.Join(root, ".isutori", "inspect", time.Now().Format("20060102T150405")+"-"+rand.Text()[:8])
	if err := os.MkdirAll(filepath.Join(dir, "snapshots"), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &Session{Dir: dir, Token: rand.Text(), Origin: u.Scheme + "://" + u.Host, ScriptPath: scriptPath, identities: make(map[string]userscript.Verification), events: f, snapshots: make(map[string]string)}, nil
}

func (s *Session) Close() error { return s.events.Close() }

func (s *Session) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/inspect/start", func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(w, r) {
			return
		}
		var request struct {
			Page   string `json:"page"`
			Source string `json:"source"`
		}
		if !strictjson.DecodeRequest(w, r, maxStartBytes, &request) {
			return
		}
		if !validPageID(request.Page) {
			http.Error(w, "invalid page ID", http.StatusBadRequest)
			return
		}
		actual, sourceErr := userscript.SourceID(request.Source)
		identity := userscript.Verification{Actual: actual}
		if sourceErr != nil {
			identity.Error = sourceErr.Error()
		}
		s.mu.Lock()
		previous, registered := s.identities[request.Page]
		if registered && (previous.Actual != identity.Actual || previous.Error != identity.Error) {
			s.mu.Unlock()
			http.Error(w, "page identity already registered", http.StatusConflict)
			return
		}
		if len(s.identities) >= maxPages && !registered {
			s.mu.Unlock()
			http.Error(w, "inspection capacity reached", http.StatusInternalServerError)
			return
		}
		s.identities[request.Page] = identity
		s.mu.Unlock()
		verification := userscript.ReadReference(s.ScriptPath).Compare(actual)
		if sourceErr != nil {
			verification.Error = sourceErr.Error()
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct {
			Origin     string                  `json:"origin"`
			Userscript userscript.Verification `json:"userscript"`
		}{s.Origin, verification})
	})
	mux.HandleFunc("POST /api/inspect", func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(w, r) {
			return
		}
		var event Event
		if !strictjson.DecodeRequest(w, r, maxEventBytes, &event) {
			return
		}
		if err := event.validate(s.Origin); err != nil {
			http.Error(w, "invalid inspection event: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.append(&event); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(event.Userscript)
	})
	return mux
}

func validPageID(page string) bool { return page != "" && len(page) <= maxPageIDLen }

func (e *Event) validate(origin string) error {
	u, err := url.Parse(e.URL)
	switch {
	case err != nil || u.Scheme+"://"+u.Host != origin || u.User != nil:
		return errors.New("url is not on the portal")
	case u.RawQuery != "" || u.Fragment != "":
		return errors.New("url must not carry a query or fragment")
	case !validPageID(e.Page):
		return errors.New("invalid page ID")
	case e.Sequence < 1 || e.At.IsZero():
		return errors.New("sequence and time are required")
	case e.Reason == "" || len(e.Reason) > maxReasonLen || len(e.Target) > maxTargetLen:
		return errors.New("reason or target out of range")
	case e.Visibility != "visible" && e.Visibility != "hidden":
		return errors.New("unknown visibility")
	case e.Snapshot != "" || !e.ReceivedAt.IsZero() || e.Userscript != (userscript.Verification{}):
		return errors.New("snapshot, received_at and userscript are set by the receiver")
	}
	return nil
}

func (s *Session) authorized(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get("Authorization") != "Bearer "+s.Token {
		http.Error(w, "invalid inspection token", http.StatusUnauthorized)
		return false
	}
	return true
}

func (s *Session) append(event *Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	identity, registered := s.identities[event.Page]
	if !registered || identity.Actual != event.ScriptID {
		return errors.New("userscript handshake required for this page")
	}
	event.Userscript = userscript.ReadReference(s.ScriptPath).Compare(identity.Actual)
	if identity.Actual == "" {
		event.Userscript.Error = identity.Error
	}
	snapshot := s.snapshots[event.Page]
	if event.HTML == "" && snapshot == "" {
		return errors.New("the first event on a page must contain HTML")
	}
	html := event.HTML
	if html != "" {
		snapshot = fmt.Sprintf("snapshots/%06d.html", s.count+1)
	}
	size := len(html)
	event.HTML = ""
	event.Snapshot = snapshot
	event.ReceivedAt = time.Now()
	b, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if s.count >= maxEvents || s.bytes+int64(size+len(b)+1) > maxSessionBytes {
		return errors.New("inspection capacity reached; start a new session")
	}
	if html != "" {
		if err := os.WriteFile(filepath.Join(s.Dir, snapshot), []byte(html), 0o600); err != nil {
			return err
		}
	}
	offset, err := s.events.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	if _, err = s.events.Write(append(b, '\n')); err == nil {
		err = s.events.Sync()
	}
	if err != nil {
		// A partial line must not corrupt later events after a failed save.
		_, seekErr := s.events.Seek(offset, io.SeekStart)
		return errors.Join(err, s.events.Truncate(offset), seekErr)
	}
	s.count++
	s.bytes += int64(size + len(b) + 1)
	s.snapshots[event.Page] = snapshot
	return nil
}

func Serve(ctx context.Context, ln net.Listener, s *Session) error {
	server := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		select {
		case <-ctx.Done():
			stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := server.Shutdown(stop); err != nil {
				server.Close()
			}
		case <-done:
		}
	}()
	err := server.Serve(ln)
	close(done)
	<-finished
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func Listen(addr string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if host != "127.0.0.1" {
		return nil, errors.New("inspect uses 127.0.0.1 for userscript compatibility")
	}
	return net.Listen("tcp", addr)
}
