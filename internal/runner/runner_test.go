package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/traP-jp/isutori/internal/config"
	"github.com/traP-jp/isutori/internal/queue"
	"github.com/traP-jp/isutori/internal/userscript"
)

type shellFunc func(ctx context.Context) error

func (f shellFunc) Run(ctx context.Context, _ string, _ io.Reader) ([]byte, error) {
	return nil, f(ctx)
}

func TestKeepLease(t *testing.T) {
	const ttl = time.Second
	hung := make(chan struct{})
	t.Cleanup(func() { close(hung) })
	tests := []struct {
		name     string
		renew    shellFunc
		wantLost error
	}{
		{"renewals succeed", func(context.Context) error { return nil }, nil},
		{"host unreachable", func(context.Context) error { return errors.New("connection timed out") }, errLeaseUnconfirmed},
		{"renewal never returns", func(context.Context) error { <-hung; return nil }, errLeaseUnconfirmed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := &run{
				cfg: &config.Config{Queue: config.Queue{LeaseTTL: ttl}},
				q:   &queue.Queue{Shell: tt.renew},
				log: log.New(io.Discard, "", 0),
			}
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			go r.keepLease(ctx, cancel, "entry")

			select {
			case <-ctx.Done():
				if tt.wantLost == nil {
					t.Fatalf("gave up the lease: %v", context.Cause(ctx))
				}
				if !errors.Is(context.Cause(ctx), tt.wantLost) {
					t.Fatalf("cause %v, want %v", context.Cause(ctx), tt.wantLost)
				}
			case <-time.After(ttl):
				if tt.wantLost != nil {
					t.Fatal("still holding the lease after lease_ttl without a renewal")
				}
			}
		})
	}
}

func TestHookKillsWhatIgnoresSIGTERM(t *testing.T) {
	dir := t.TempDir()
	r := &run{
		cfg:    &config.Config{Hooks: config.Hooks{Timeout: 300 * time.Millisecond}},
		opsDir: dir,
		out:    io.Discard,
		log:    log.New(io.Discard, "", 0),
	}
	err := r.hook(`(trap '' TERM; exec sleep 30) >/dev/null 2>&1 & echo $! > pid; wait`)(context.Background())
	if err == nil {
		t.Fatal("hook outlived hooks.timeout")
	}
	b, err := os.ReadFile(filepath.Join(dir, "pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	time.Sleep(100 * time.Millisecond)
	if err := syscall.Kill(pid, 0); err == nil {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Fatal("a child that ignored SIGTERM survived the hook")
	}
}

func TestReadBenchResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), benchResultFile)
	if _, err := readBenchResult(path); err == nil {
		t.Error("a missing result was accepted")
	}
	for body, wantErr := range map[string]bool{
		`{"passed":true,"score":42,"message":"ok"}`: false,
		`{"passed":false}`:                          false,
		`{"pass":true,"score":42}`:                  true,
		`{"score":42}`:                              true,
		`not json`:                                  true,
		`{"passed":true} {"passed":false}`:          true,
	} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		res, err := readBenchResult(path)
		if (err != nil) != wantErr {
			t.Errorf("%s: got error %v, want error %v", body, err, wantErr)
		}
		if body == `{"passed":true,"score":42,"message":"ok"}` && *res != (BenchResult{Passed: true, Score: 42, Message: "ok"}) {
			t.Errorf("%s: got %+v", body, *res)
		}
	}
}

func TestFinishPublishesAfterFailure(t *testing.T) {
	for _, failed := range []string{"", "collect", "analyze"} {
		t.Run(failed, func(t *testing.T) {
			dir := t.TempDir()
			r := &run{
				cfg: &config.Config{Hooks: config.Hooks{
					Timeout: time.Second,
					Collect: "echo collect >> steps",
					Analyze: "echo analyze >> steps",
					Publish: `echo publish >> steps; cp "$ISUTORI_RUN_DIR/run.json" shared.json`,
				}},
				opsDir: dir, env: append(os.Environ(), "ISUTORI_RUN_DIR="+dir),
				out: io.Discard, log: log.New(io.Discard, "", 0),
				summary: &Summary{RunDir: dir, Bench: &BenchResult{Passed: true, Score: 435581}},
			}
			if failed == "collect" {
				r.cfg.Hooks.Collect += "; exit 1"
			} else if failed == "analyze" {
				r.cfg.Hooks.Analyze += "; exit 1"
			}
			err := r.finish(context.Background())
			if (err != nil) != (failed != "") {
				t.Fatalf("finish: %v", err)
			}
			b, err := os.ReadFile(filepath.Join(dir, "shared.json"))
			if err != nil {
				t.Fatal(err)
			}
			var shared Summary
			if err := json.Unmarshal(b, &shared); err != nil {
				t.Fatal(err)
			}
			if shared.Bench == nil || shared.Bench.Score != 435581 || (shared.Error != "") != (failed != "") {
				t.Fatalf("shared result: %+v", shared)
			}
			steps, err := os.ReadFile(filepath.Join(dir, "steps"))
			if err != nil {
				t.Fatal(err)
			}
			want := "collect\nanalyze\npublish\n"
			if failed == "collect" {
				want = "collect\npublish\n"
			}
			if string(steps) != want {
				t.Fatalf("steps = %q, want %q", steps, want)
			}
		})
	}
}

func TestPublishSavedResult(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".isutori", "runs", "saved-run")
	ops := filepath.Join(dir, "ops")
	if err := os.MkdirAll(ops, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `[queue]
host = "must-not-be-contacted"
[hooks]
deploy = "exit 99"
bench = "exit 99"
collect = "exit 99"
analyze = "exit 99"
cleanup = "exit 99"
publish = 'test "$ISUTORI_APP_COMMIT" = app && test "$ISUTORI_OPS_COMMIT" = ops && test "$ISUTORI_MODE" = final && test "$ISUTORI_REQUESTER" = tester && test "$ISUTORI_RUN_ID" = saved-run && mkdir -p "$ISUTORI_REPO/shared/$ISUTORI_RUN_ID" && cp "$ISUTORI_RUN_DIR/run.json" "$ISUTORI_REPO/shared/$ISUTORI_RUN_ID/run.json" && test -f "$ISUTORI_RUN_DIR/allow-publish"'
`
	if err := os.WriteFile(filepath.Join(ops, config.FileName), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	saved := Summary{
		RunID: "saved-run", RunDir: dir, OpsCommit: "ops", Error: "collect: interrupted",
		Entry:  queue.Entry{AppCommit: "app", Mode: ModeFinal, Requester: "tester"},
		Stages: []Stage{{Name: "bench", Status: "running"}},
	}
	if err := writeJSON(filepath.Join(dir, "run.json"), saved); err != nil {
		t.Fatal(err)
	}
	proof := userscript.Verification{Expected: "sha256:expected", Actual: "sha256:expected", Status: "matched"}
	if err := writeJSON(filepath.Join(dir, benchResultFile), BenchResult{Passed: true, Score: 435581, Userscript: proof}); err != nil {
		t.Fatal(err)
	}
	lock, err := lockRun(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Publish(context.Background(), dir, io.Discard); err == nil {
		t.Fatal("published a run that is still in use")
	}
	lock.Close()
	if _, err := Publish(context.Background(), dir, io.Discard); err == nil {
		t.Fatal("ignored a failed publish hook")
	}
	if err := os.WriteFile(filepath.Join(dir, "allow-publish"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		summary, err := Publish(context.Background(), dir, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if summary.Bench.Score != 435581 || summary.Bench.Userscript != proof || summary.Error != saved.Error || summary.Stages[0].Status != "unknown" {
			t.Fatalf("lost the original result or failure: %+v", summary)
		}
	}
	b, err := os.ReadFile(filepath.Join(root, "shared", "saved-run", "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var shared Summary
	if err := json.Unmarshal(b, &shared); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(root, "shared", "*", "run.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("retry duplicated the shared record: %v, %v", files, err)
	}
	if shared.RunID != saved.RunID || shared.Bench == nil || shared.Bench.Score != 435581 || shared.Bench.Userscript != proof {
		t.Fatalf("shared result: %+v", shared)
	}
	if err := SaveBenchResult(filepath.Join(dir, benchResultFile), BenchResult{Passed: true, Score: 999}); err != nil {
		t.Fatal(err)
	}
	if _, err := Publish(context.Background(), dir, io.Discard); err == nil {
		t.Fatal("replaced a saved result with a different result under the same run ID")
	}
	if err := os.WriteFile(filepath.Join(dir, benchResultFile), []byte(`{"pass":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Publish(context.Background(), dir, io.Discard); err == nil {
		t.Fatal("published an invalid benchmark result")
	}
}

func TestFinishDoesNotPublishAfterCancellation(t *testing.T) {
	dir := t.TempDir()
	r := &run{
		cfg:    &config.Config{Hooks: config.Hooks{Timeout: time.Second, Publish: "touch shared"}},
		opsDir: dir, out: io.Discard, log: log.New(io.Discard, "", 0),
		summary: &Summary{RunDir: dir, Bench: &BenchResult{Passed: true}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.finish(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("finish: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "shared")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("publish ran after cancellation: %v", err)
	}
}

func TestWriteJSONPreservesPreviousRecordOnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.json")
	if err := writeJSON(path, "saved"); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(path, make(chan int)); err == nil {
		t.Fatal("accepted a value that cannot be encoded")
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "\"saved\"\n" {
		t.Fatalf("previous record was lost: %q, %v", b, err)
	}
}

func TestExecuteKeepsBenchmarkAndCleansUp(t *testing.T) {
	for _, failure := range []string{"", "bench", "collect", "analyze", "publish", "collect+publish", "collect timeout", "cleanup", "record", "record+cleanup"} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			hooks := config.Hooks{
				Timeout: 500 * time.Millisecond, BenchTimeout: time.Second,
				Deploy:  ":",
				Bench:   `printf '{"passed":true,"score":435581,"message":"ok"}' > "$ISUTORI_BENCH_RESULT"`,
				Collect: ":", Analyze: ":",
				Publish: `cp "$ISUTORI_RUN_DIR/run.json" shared.json`,
				Cleanup: "touch cleaned",
			}
			switch failure {
			case "bench":
				hooks.Bench = "exit 11"
			case "collect", "collect+publish":
				hooks.Collect = "exit 12"
			case "analyze":
				hooks.Analyze = "exit 13"
			case "collect timeout":
				hooks.Collect = "sleep 30"
			case "record", "record+cleanup":
				hooks.Collect = `mv "$ISUTORI_RUN_DIR/run.json" "$ISUTORI_RUN_DIR/run.saved.json"; mkdir "$ISUTORI_RUN_DIR/run.json"`
			}
			if strings.Contains(failure, "cleanup") {
				hooks.Cleanup += "; exit 15"
			}
			if failure == "publish" || failure == "collect+publish" {
				hooks.Publish += "; exit 14"
			}
			r := &run{
				cfg:    &config.Config{Queue: config.Queue{LeaseTTL: 10 * time.Second}, Hooks: hooks},
				q:      &queue.Queue{Shell: queue.Local{}, Dir: filepath.Join(dir, "queue"), TTL: 10 * time.Second},
				opsDir: dir, out: io.Discard, log: log.New(io.Discard, "", 0),
				summary: &Summary{RunID: "run1", RunDir: dir, Entry: queue.Entry{RunID: "run1", AppCommit: "app1"}},
			}
			r.env = environment(r.summary, dir, "unused", "unused")
			err := r.execute(context.Background(), context.Background())
			if (err != nil) != (failure != "") {
				t.Fatalf("execute: %v", err)
			}
			if failure == "collect+publish" && (!strings.Contains(err.Error(), "collect") || !strings.Contains(err.Error(), "publish")) {
				t.Fatalf("lost one of the failures: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "cleaned")); err != nil {
				t.Fatalf("cleanup was skipped: %v", err)
			}
			entries, queueErr := r.q.List(context.Background(), false)
			if queueErr != nil || len(entries) != 0 {
				t.Fatalf("queue was not released: %+v, %v", entries, queueErr)
			}
			if strings.HasPrefix(failure, "record") {
				if !strings.Contains(err.Error(), "after collect") || !strings.Contains(err.Error(), "before cleanup") {
					t.Fatalf("lost a record write error: %v", err)
				}
				last := r.summary.Stages[len(r.summary.Stages)-1]
				wantStatus := "succeeded"
				if failure == "record+cleanup" {
					wantStatus = "failed"
					if !strings.Contains(err.Error(), "cleanup: exit status 15") {
						t.Fatalf("lost the cleanup error: %v", err)
					}
				}
				if last.Name != "cleanup" || last.Status != wantStatus {
					t.Fatalf("cleanup outcome: %+v", last)
				}
			}
			if failure == "bench" || strings.HasPrefix(failure, "record") {
				if _, err := os.Stat(filepath.Join(dir, "shared.json")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("published after failure %q: %v", failure, err)
				}
				return
			}
			var shared Summary
			b, err := os.ReadFile(filepath.Join(dir, "shared.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(b, &shared); err != nil {
				t.Fatal(err)
			}
			if shared.Bench == nil || !shared.Bench.Passed || shared.Bench.Score != 435581 || shared.Entry.AppCommit != "app1" {
				t.Fatalf("lost benchmark identity or result: %+v", shared)
			}
			last := r.summary.Stages[len(r.summary.Stages)-2]
			if last.Name != "publish" || (last.Status == "failed") != strings.Contains(failure, "publish") {
				t.Fatalf("publication outcome: %+v", last)
			}
			for _, stage := range shared.Stages {
				if stage.Name == "publish" && stage.Status != "running" {
					t.Fatalf("claimed publication success before the hook returned: %+v", stage)
				}
			}
		})
	}
}

func TestStageRecordsProgressBeforeRunning(t *testing.T) {
	dir := t.TempDir()
	r := &run{summary: &Summary{RunDir: dir}, log: log.New(io.Discard, "", 0)}
	if err := r.stage(context.Background(), "publish", func(context.Context) error {
		b, err := os.ReadFile(filepath.Join(dir, "run.json"))
		if err != nil {
			return err
		}
		var saved Summary
		if err := json.Unmarshal(b, &saved); err != nil {
			return err
		}
		if len(saved.Stages) != 1 || saved.Stages[0].Status != "running" {
			t.Fatalf("no durable progress record: %+v", saved)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if r.summary.Stages[0].Status != "succeeded" {
		t.Fatal("did not record completion")
	}
	if err := r.stage(context.Background(), "analyze", nil); err != nil {
		t.Fatal(err)
	}
	if r.summary.Stages[1].Status != "skipped" {
		t.Fatal("an unconfigured hook was reported as successful")
	}
	r.summary.RunDir = filepath.Join(dir, "missing")
	called := false
	if err := r.stage(context.Background(), "publish", func(context.Context) error { called = true; return nil }); err == nil || called {
		t.Fatalf("ran without saving progress: called=%v, error=%v", called, err)
	}
}
