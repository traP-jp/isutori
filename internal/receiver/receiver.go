package receiver

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"mime"
	"net"
	"net/http"
	"time"
)

type Result struct {
	RunID   string `json:"run_id"`
	Passed  bool   `json:"passed"`
	Score   int64  `json:"score"`
	Message string `json:"message"`
	// Set when no result could be obtained, as opposed to a failed benchmark.
	Error      string    `json:"error,omitempty"`
	ReceivedAt time.Time `json:"received_at"`
}

//go:embed manual.html
var manualHTML string

var manualTmpl = template.Must(template.New("manual").Parse(manualHTML))

// Wait serves the result endpoint on ln until a result for runID arrives, then
// closes ln.
func Wait(ctx context.Context, ln net.Listener, runID, target string) (Result, error) {
	results := make(chan Result, 1)
	srv := &http.Server{Handler: handler(runID, target, results), ReadHeaderTimeout: 10 * time.Second}
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

func handler(runID, target string, results chan<- Result) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		manualTmpl.Execute(w, map[string]string{"RunID": runID, "Target": target})
	})
	mux.HandleFunc("GET /api/run", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"run_id": runID})
	})
	mux.HandleFunc("POST /api/result", func(w http.ResponseWriter, r *http.Request) {
		// Requiring JSON forces a CORS preflight, which this server never answers,
		// so arbitrary web pages cannot submit results. Userscripts bypass CORS.
		if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
			http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
			return
		}
		var res Result
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&res); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if res.RunID != runID {
			http.Error(w, fmt.Sprintf("run_id mismatch: waiting for %q", runID), http.StatusConflict)
			return
		}
		res.ReceivedAt = time.Now()
		select {
		case results <- res:
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "result already received", http.StatusConflict)
		}
	})
	return mux
}
