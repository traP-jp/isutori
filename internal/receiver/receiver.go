package receiver

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"github.com/traP-jp/isutori/internal/userscript"
	"html/template"
	"io"
	"mime"
	"net"
	"net/http"
	"sync"
	"time"
)

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

//go:embed manual.html
var manualHTML string

var manualTmpl = template.Must(template.New("manual").Parse(manualHTML))

// Wait serves the result endpoint on ln until a result for runID arrives, then
// closes ln. It calls save before acknowledging receipt; a failed save permits retry.
func Wait(ctx context.Context, ln net.Listener, runID, scriptPath string, out io.Writer, save func(Result) error) (Result, error) {
	results := make(chan Result, 1)
	srv := &http.Server{Handler: handler(runID, results, scriptPath, out, save), ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	select {
	case r := <-results:
		return r, nil
	case <-ctx.Done():
		return Result{}, context.Cause(ctx)
	}
}

func handler(runID string, results chan<- Result, scriptPath string, out io.Writer, save func(Result) error) http.Handler {
	var mu sync.Mutex
	accepted := false
	reference := userscript.ReadReference(scriptPath)
	var approved userscript.Verification
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		manualTmpl.Execute(w, map[string]string{"RunID": runID})
	})
	mux.HandleFunc("GET /api/run", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"run_id": runID})
	})
	mux.HandleFunc("POST /api/userscript", func(w http.ResponseWriter, r *http.Request) {
		if scriptPath == "" {
			http.Error(w, "not an autobench receiver", http.StatusConflict)
			return
		}
		if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
			http.Error(w, "expected application/json", 415)
			return
		}
		var request struct {
			RunID  string `json:"run_id"`
			Source string `json:"source"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&request); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if err := dec.Decode(new(any)); err != io.EOF {
			http.Error(w, "expected one JSON object", 400)
			return
		}
		if request.RunID != runID {
			http.Error(w, "run_id mismatch", 409)
			return
		}
		verification := reference.Verify(request.Source)
		mu.Lock()
		if verification.Status == "matched" {
			approved = verification
		}
		mu.Unlock()
		fmt.Fprintf(out, "isutori: userscript %s: expected=%s actual=%s file=%s %s\n", verification.Status, verification.Expected, verification.Actual, scriptPath, verification.Error)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(verification)
	})
	deliver := func(manual bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			// Requiring JSON forces a CORS preflight, which this server never answers,
			// so arbitrary web pages cannot submit results. Userscripts bypass CORS.
			if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
				http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
			var res Result
			dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&res); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if err := dec.Decode(new(any)); err != io.EOF {
				http.Error(w, "expected one JSON object", 400)
				return
			}
			if res.RunID != runID {
				http.Error(w, fmt.Sprintf("run_id mismatch: waiting for %q", runID), http.StatusConflict)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if accepted {
				http.Error(w, "result already received", http.StatusConflict)
				return
			}
			if manual || scriptPath == "" {
				res.Userscript = userscript.Verification{Status: "not_checked"}
			} else {
				if approved.Status != "matched" || res.Userscript.Actual != approved.Actual {
					http.Error(w, "userscript verification required; use the manual form if needed", 409)
					return
				}
				res.Userscript = approved
			}
			res.ReceivedAt = time.Now()
			if err := save(res); err != nil {
				http.Error(w, "failed to save result: "+err.Error(), http.StatusInternalServerError)
				return
			}
			accepted = true
			results <- res
			w.WriteHeader(http.StatusNoContent)
		}
	}
	mux.HandleFunc("POST /api/result", deliver(false))
	mux.HandleFunc("POST /api/manual-result", deliver(true))
	return mux
}
