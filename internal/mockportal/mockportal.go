// Package mockportal serves the contestant UI of isucon14-portal backed by
// fake in-browser data, for developing userscripts without a real portal.
package mockportal

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"time"
)

type Outcome string

const (
	Pass  Outcome = "pass"
	Fail  Outcome = "fail"
	Error Outcome = "error"
)

// Config is handed to the frontend; field names must match MockConfig in
// mockportal/src/main.tsx.
type Config struct {
	Pending time.Duration
	Running time.Duration
	Outcome Outcome
	Score   int64
}

//go:embed dist
var dist embed.FS

//go:embed index.html
var indexHTML string

var indexTmpl = template.Must(template.New("index").Parse(indexHTML))

func Handler(c Config) (http.Handler, error) {
	switch c.Outcome {
	case Pass, Fail, Error:
	default:
		return nil, fmt.Errorf("outcome must be %q, %q or %q, got %q", Pass, Fail, Error, c.Outcome)
	}
	frontendConfig, err := json.Marshal(map[string]any{
		"pendingSeconds": c.Pending.Seconds(),
		"runningSeconds": c.Running.Seconds(),
		"outcome":        c.Outcome,
		"score":          c.Score,
	})
	if err != nil {
		return nil, err
	}
	assets, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServerFS(assets)))
	mux.Handle("GET /{$}", http.RedirectHandler("/contestant", http.StatusFound))
	mux.HandleFunc("GET /sw.js", http.NotFound)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		indexTmpl.Execute(w, string(frontendConfig))
	})
	return mux, nil
}
