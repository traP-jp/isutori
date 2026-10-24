package receiver

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/traP-jp/isutori/internal/strictjson"
	"github.com/traP-jp/isutori/internal/userscript"
)

const maxRequestBytes = 1 << 20

type Result struct {
	RunID   string `json:"run_id"`
	Passed  bool   `json:"passed"`
	Score   int64  `json:"score"`
	Message string `json:"message"`
	// Set when no result could be obtained, as opposed to a failed benchmark.
	Error      string                  `json:"error,omitempty"`
	ReceivedAt time.Time               `json:"received_at"`
	Userscript userscript.Verification `json:"userscript,omitzero"`
}

type Options struct {
	RunID string
	// ScriptPath is the userscript that automatic results must match; empty
	// accepts only manual results.
	ScriptPath string
	Log        io.Writer
	// Save runs before receipt is acknowledged; an error lets the sender retry.
	Save func(Result) error
}

//go:embed manual.html
var manualHTML string

var manualTmpl = template.Must(template.New("manual").Parse(manualHTML))

// Wait serves the result endpoints on ln until a result for opts.RunID
// arrives, then closes ln.
func Wait(ctx context.Context, ln net.Listener, opts Options) (Result, error) {
	rec := newReceiver(opts)
	srv := &http.Server{Handler: rec.handler(), ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	select {
	case r := <-rec.results:
		return r, nil
	case <-ctx.Done():
		return Result{}, context.Cause(ctx)
	}
}

type receiver struct {
	Options
	reference userscript.Reference
	results   chan Result

	mu       sync.Mutex
	accepted bool
	approved userscript.Verification
}

func newReceiver(opts Options) *receiver {
	return &receiver{
		Options:   opts,
		reference: userscript.ReadReference(opts.ScriptPath),
		results:   make(chan Result, 1),
	}
}

func (rec *receiver) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		manualTmpl.Execute(w, map[string]string{"RunID": rec.RunID})
	})
	mux.HandleFunc("GET /api/run", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"run_id": rec.RunID})
	})
	mux.HandleFunc("POST /api/userscript", rec.verify)
	mux.HandleFunc("POST /api/result", rec.deliver(false))
	mux.HandleFunc("POST /api/manual-result", rec.deliver(true))
	return mux
}

func (rec *receiver) verify(w http.ResponseWriter, r *http.Request) {
	if rec.ScriptPath == "" {
		http.Error(w, "not an autobench receiver", http.StatusConflict)
		return
	}
	var request struct {
		RunID  string `json:"run_id"`
		Source string `json:"source"`
	}
	if !strictjson.DecodeRequest(w, r, maxRequestBytes, &request) {
		return
	}
	if request.RunID != rec.RunID {
		http.Error(w, "run_id mismatch", http.StatusConflict)
		return
	}
	v := rec.reference.Verify(request.Source)
	if v.Status == userscript.Matched {
		rec.mu.Lock()
		rec.approved = v
		rec.mu.Unlock()
	}
	fmt.Fprintf(rec.Log, "isutori: userscript %s: expected=%s actual=%s file=%s %s\n", v.Status, v.Expected, v.Actual, rec.ScriptPath, v.Error)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (rec *receiver) deliver(manual bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var res Result
		if !strictjson.DecodeRequest(w, r, maxRequestBytes, &res) {
			return
		}
		if res.RunID != rec.RunID {
			http.Error(w, fmt.Sprintf("run_id mismatch: waiting for %q", rec.RunID), http.StatusConflict)
			return
		}
		rec.mu.Lock()
		defer rec.mu.Unlock()
		if rec.accepted {
			http.Error(w, "result already received", http.StatusConflict)
			return
		}
		switch {
		case manual || rec.ScriptPath == "":
			res.Userscript = userscript.Verification{Status: userscript.NotChecked}
		case rec.approved.Status != userscript.Matched || res.Userscript.Actual != rec.approved.Actual:
			http.Error(w, "userscript verification required; use the manual form if needed", http.StatusConflict)
			return
		default:
			res.Userscript = rec.approved
		}
		res.ReceivedAt = time.Now()
		if err := rec.Save(res); err != nil {
			http.Error(w, "failed to save result: "+err.Error(), http.StatusInternalServerError)
			return
		}
		rec.accepted = true
		rec.results <- res
		w.WriteHeader(http.StatusNoContent)
	}
}
