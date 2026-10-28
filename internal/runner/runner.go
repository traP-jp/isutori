package runner

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/traP-jp/isutori/internal/config"
	"github.com/traP-jp/isutori/internal/queue"
	"github.com/traP-jp/isutori/internal/strictjson"
	"github.com/traP-jp/isutori/internal/userscript"
)

const (
	ModeMeasure = "measure"
	ModeFinal   = "final"
)

type Options struct {
	// Abort, once done, kills the running hook and skips cleanup.
	Abort     context.Context
	RepoRoot  string
	Ref       string
	Mode      string
	Requester string
	Log       io.Writer
}

// Stage statuses recorded in run.json.
const (
	StageRunning   = "running"
	StageSucceeded = "succeeded"
	StageFailed    = "failed"
	StageSkipped   = "skipped"
	// StageUnknown replaces StageRunning when a retry finds no end record.
	StageUnknown = "unknown"
)

const stageCleanup = "cleanup"

type Stage struct {
	Name    string  `json:"name"`
	Status  string  `json:"status,omitempty"`
	Seconds float64 `json:"seconds"`
	Error   string  `json:"error,omitempty"`
}

type Summary struct {
	RunID     string       `json:"run_id"`
	RunDir    string       `json:"run_dir"`
	Entry     queue.Entry  `json:"entry"`
	OpsCommit string       `json:"ops_commit"`
	Bench     *BenchResult `json:"bench,omitempty"`
	Stages    []Stage      `json:"stages"`
	Error     string       `json:"error,omitempty"`
}

type run struct {
	cfg     *config.Config
	q       *queue.Queue
	opsDir  string
	env     []string
	log     *log.Logger
	out     io.Writer
	summary *Summary
}

// Run deploys opts.Ref and benchmarks it at the head of the queue.
func Run(ctx context.Context, opts Options) (summary *Summary, err error) {
	root := opts.RepoRoot
	runID := time.Now().Format("20060102T150405") + "-" + rand.Text()[:8]
	runDir := runDirOf(root, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, err
	}
	logFile, err := os.Create(filepath.Join(runDir, "run.log"))
	if err != nil {
		return nil, err
	}
	defer logFile.Close()
	defer func() {
		if err != nil && summary == nil {
			fmt.Fprintf(logFile, "FAILED preparation: %v\n", err)
			err = fmt.Errorf("prepare run (log: %s): %w", filepath.Join(runDir, "run.log"), err)
		}
	}()
	out := io.MultiWriter(opts.Log, logFile)
	prepLog := log.New(out, "isutori: ", log.Ltime)
	prepLog.Printf("preparing run %s in %s", runID, runDir)
	defer cleanupRefs(root, runID, prepLog)
	refs, err := prepareRefs(ctx, root, runID, opts.Ref, prepLog)
	if err != nil {
		return nil, err
	}
	opsDir := filepath.Join(runDir, "ops")
	if err := os.MkdirAll(opsDir, 0o755); err != nil {
		return nil, err
	}
	lock, err := lockRun(runDir)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err := extract(ctx, root, refs.Ops, opsDir); err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(opsDir, config.FileName)); errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%s is not on %s; commit it and push it there first", config.FileName, refs.Ops)
	}
	r, err := loadRun(root, &Summary{
		RunID:  runID,
		RunDir: runDir,
		Entry: queue.Entry{
			RunID:      runID,
			Requester:  opts.Requester,
			Ref:        refs.Name,
			AppCommit:  refs.App,
			Mode:       opts.Mode,
			EnqueuedAt: time.Now(),
		},
		OpsCommit: refs.Ops,
	}, out)
	if err != nil {
		return nil, err
	}
	if err := r.writeSummary(); err != nil {
		return r.summary, err
	}
	err = r.execute(ctx, opts.Abort)
	if err != nil {
		r.summary.Error = err.Error()
	}
	return r.summary, errors.Join(err, r.writeSummary())
}

var errAborted = errors.New("aborted by a second interrupt")

func (r *run) execute(parent, abort context.Context) error {
	name, err := r.q.Enqueue(parent, r.summary.Entry)
	if err != nil {
		return err
	}
	r.log.Printf("enqueued %s", name)
	defer func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), r.cfg.Queue.LeaseTTL/4)
		defer cancel()
		if err := r.q.Remove(ctx, name); err != nil {
			r.log.Printf("WARNING: failed to remove queue entry %s; run `isutori queue clear` if it blocks others: %v", name, err)
		}
	}()

	// The lease outlives Ctrl-C so that cleanup still holds the servers.
	leaseCtx, loseLease := context.WithCancelCause(context.WithoutCancel(parent))
	defer loseLease(nil)
	go r.keepLease(leaseCtx, loseLease, name)
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	defer context.AfterFunc(leaseCtx, func() { cancel(context.Cause(leaseCtx)) })()
	defer context.AfterFunc(abort, func() { loseLease(errAborted) })()
	defer context.AfterFunc(parent, func() {
		r.log.Print("interrupted; cleanup still runs unless you interrupt again")
	})()

	if err := r.waitHead(ctx, name); err != nil {
		return err
	}

	h := r.cfg.Hooks
	state := "healthy"
	if h.Health == "" {
		state = "deployed"
	}
	steps := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"deploy", r.deploy},
		{"health", r.hook(h.Health)},
		{"record", r.recordDeployed(state)},
		{"prepare", r.hook(h.Prepare)},
		{"bench", r.bench},
	}
	var runErr error
	for _, s := range steps {
		if runErr = r.stage(ctx, s.name, s.fn); runErr != nil {
			break
		}
	}

	if runErr == nil {
		runErr = r.finish(ctx)
	}

	// A stage may have failed because the queue entry is gone, e.g. guard in
	// deploy; find out now rather than at the next renewal.
	renewCtx, cancelRenew := context.WithTimeout(leaseCtx, r.cfg.Queue.LeaseTTL/4)
	if err := r.q.Renew(renewCtx, name); errors.Is(err, queue.ErrEntryLost) {
		loseLease(err)
	}
	cancelRenew()
	switch cause := context.Cause(leaseCtx); {
	case errors.Is(cause, errAborted):
		return errors.Join(runErr, cause)
	case errors.Is(cause, queue.ErrEntryLost), errors.Is(cause, errLeaseUnconfirmed):
		r.log.Print("WARNING: skipping cleanup because the servers may already belong to the next run")
		return errors.Join(runErr, cause)
	}
	return errors.Join(runErr, r.stage(leaseCtx, stageCleanup, r.hook(h.Cleanup)))
}

func (r *run) finish(ctx context.Context) error {
	var runErr error
	for _, step := range []struct{ name, command string }{
		{"collect", r.cfg.Hooks.Collect},
		{"analyze", r.cfg.Hooks.Analyze},
	} {
		if runErr = r.stage(ctx, step.name, r.hook(step.command)); runErr != nil {
			break
		}
	}
	if ctx.Err() != nil {
		return errors.Join(runErr, context.Cause(ctx))
	}
	if runErr != nil {
		// publish shares run.json, so it must already show the failure.
		r.summary.Error = runErr.Error()
	}
	return errors.Join(runErr, r.stage(ctx, "publish", r.hook(r.cfg.Hooks.Publish)))
}

var errLeaseUnconfirmed = errors.New("lease renewals kept failing; the queue host may have handed the servers to the next run")

func (r *run) keepLease(ctx context.Context, cancel context.CancelCauseFunc, name string) {
	ttl := r.cfg.Queue.LeaseTTL
	// Stop a quarter before the host may prune the entry, on a timer of its
	// own so that a renewal that never returns cannot delay it.
	giveUp := ttl * 3 / 4
	deadline := time.AfterFunc(giveUp, func() {
		r.log.Printf("ERROR: %v", errLeaseUnconfirmed)
		cancel(errLeaseUnconfirmed)
	})
	defer deadline.Stop()
	t := time.NewTicker(ttl / 4)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		start := time.Now()
		renewCtx, cancelRenew := context.WithTimeout(ctx, ttl/4)
		err := r.q.Renew(renewCtx, name)
		cancelRenew()
		if ctx.Err() != nil {
			return
		}
		switch {
		case errors.Is(err, queue.ErrEntryLost):
			r.log.Printf("ERROR: %v", err)
			cancel(err)
			return
		case err != nil:
			r.log.Printf("WARNING: lease renewal failed, retrying: %v", err)
		default:
			deadline.Reset(time.Until(start.Add(giveUp)))
		}
	}
}

func (r *run) waitHead(ctx context.Context, name string) error {
	last := ""
	for {
		entries, err := r.q.List(ctx, true)
		if err != nil {
			return err
		}
		pos := -1
		for i, e := range entries {
			if e.Name == name {
				pos = i
			}
		}
		switch pos {
		case -1:
			return queue.ErrEntryLost
		case 0:
			r.log.Print("reached the head of the queue")
			return nil
		}
		if head := entries[0]; head.RunID != last {
			r.log.Printf("waiting: %d ahead, now running %s by %s", pos, head.Ref, head.Requester)
			last = head.RunID
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(3 * time.Second):
		}
	}
}

func (r *run) stage(ctx context.Context, name string, fn func(context.Context) error) error {
	r.log.Printf("== %s", name)
	index := len(r.summary.Stages)
	r.summary.Stages = append(r.summary.Stages, Stage{Name: name, Status: StageRunning})
	if fn == nil {
		r.summary.Stages[index].Status = StageSkipped
		return r.writeSummary()
	}
	saveErr := r.writeSummary()
	if saveErr != nil {
		saveErr = fmt.Errorf("before %s: %w", name, saveErr)
		// Local recording failures must not prevent server cleanup.
		if name != stageCleanup {
			return saveErr
		}
	}
	start := time.Now()
	err := fn(ctx)
	s := &r.summary.Stages[index]
	s.Seconds = time.Since(start).Seconds()
	s.Status = StageSucceeded
	if err != nil {
		s.Status = StageFailed
		s.Error = err.Error()
		err = fmt.Errorf("%s: %w", name, err)
		r.log.Printf("FAILED %v", err)
	}
	if endErr := r.writeSummary(); endErr != nil {
		err = errors.Join(err, fmt.Errorf("after %s: %w", name, endErr))
	}
	return errors.Join(saveErr, err)
}

func (r *run) hook(cmdline string) func(context.Context) error {
	if cmdline == "" {
		return nil
	}
	return func(ctx context.Context) error {
		return r.sh(ctx, cmdline, "hooks.timeout", r.cfg.Hooks.Timeout)
	}
}

func (r *run) sh(ctx context.Context, cmdline, timeoutKey string, timeout time.Duration) error {
	if cmdline == "" {
		r.log.Print("(no hook configured)")
		return nil
	}
	ctx, cancel := context.WithTimeoutCause(ctx, timeout, fmt.Errorf("exceeded %s (%v)", timeoutKey, timeout))
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", cmdline)
	cmd.Dir = r.opsDir
	cmd.Env = r.env
	cmd.Stdout = r.out
	cmd.Stderr = r.out
	// Signal the whole process group so that ansible, ssh and the like
	// spawned by the hook do not outlive it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		sig := syscall.SIGTERM
		if errors.Is(context.Cause(ctx), errAborted) {
			sig = syscall.SIGKILL
		}
		return syscall.Kill(-cmd.Process.Pid, sig)
	}
	cmd.WaitDelay = 10 * time.Second
	err := cmd.Run()
	if ctx.Err() != nil && cmd.Process != nil {
		// The group may ignore Cancel's SIGTERM, and WaitDelay kills only the shell.
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if err != nil {
		return errors.Join(err, context.Cause(ctx))
	}
	return nil
}

// deploy records the deploy before starting it, so that a deploy that fails
// halfway still shows up in `isutori status`.
func (r *run) deploy(ctx context.Context) error {
	if err := r.recordDeployed("deploying")(ctx); err != nil {
		return err
	}
	return r.hook(r.cfg.Hooks.Deploy)(ctx)
}

func (r *run) recordDeployed(state string) func(context.Context) error {
	return func(ctx context.Context) error {
		e := r.summary.Entry
		body, err := json.Marshal(map[string]any{
			"state":       state,
			"run_id":      r.summary.RunID,
			"by":          e.Requester,
			"ref":         e.Ref,
			"app_commit":  e.AppCommit,
			"ops_commit":  r.summary.OpsCommit,
			"mode":        e.Mode,
			"deployed_at": time.Now(),
		})
		if err != nil {
			return err
		}
		return r.q.WriteDeployed(ctx, body)
	}
}

func (r *run) bench(ctx context.Context) error {
	h := r.cfg.Hooks
	if err := r.sh(ctx, h.Bench, "hooks.bench_timeout", h.BenchTimeout); err != nil {
		return err
	}
	res, err := readBenchResult(filepath.Join(r.summary.RunDir, benchResultFile))
	if err != nil {
		return err
	}
	r.summary.Bench = res
	r.log.Printf("bench result: passed=%v score=%d", res.Passed, res.Score)
	return nil
}

// BenchResult is what hooks.bench writes to $ISUTORI_BENCH_RESULT.
type BenchResult struct {
	Passed     bool                    `json:"passed"`
	Score      int64                   `json:"score"`
	Message    string                  `json:"message"`
	Userscript userscript.Verification `json:"userscript,omitzero"`
}

// SaveBenchResult durably replaces the result file before returning success.
func SaveBenchResult(path string, result BenchResult) error {
	return writeJSON(path, result)
}

const benchResultFile = "bench.json"

func readBenchResult(path string) (*BenchResult, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("hooks.bench exited without writing $ISUTORI_BENCH_RESULT")
	}
	if err != nil {
		return nil, err
	}
	// A typo such as "pass" must not read as a failed benchmark.
	var v struct {
		BenchResult
		Passed *bool `json:"passed"`
	}
	if err := strictjson.Decode(bytes.NewReader(b), &v); err != nil {
		return nil, fmt.Errorf("$ISUTORI_BENCH_RESULT: %w", err)
	}
	if v.Passed == nil {
		return nil, errors.New(`$ISUTORI_BENCH_RESULT: "passed" is missing`)
	}
	v.BenchResult.Passed = *v.Passed
	return &v.BenchResult, nil
}

func (r *run) writeSummary() error {
	return writeJSON(filepath.Join(r.summary.RunDir, "run.json"), r.summary)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func runDirOf(root, runID string) string {
	return filepath.Join(root, ".isutori", "runs", runID)
}

func repoRootOf(runDir string) (string, error) {
	runs := filepath.Dir(runDir)
	if filepath.Base(runs) != "runs" || filepath.Base(filepath.Dir(runs)) != ".isutori" {
		return "", errors.New("not a run directory under .isutori/runs; use the original directory")
	}
	return filepath.Dir(filepath.Dir(runs)), nil
}

// loadRun wires a run to the configuration and hooks saved in its directory.
func loadRun(root string, summary *Summary, out io.Writer) (*run, error) {
	opsDir := filepath.Join(summary.RunDir, "ops")
	cfgPath := filepath.Join(opsDir, config.FileName)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("%s at ops ref %s: %w", config.FileName, summary.OpsCommit, err)
	}
	binDir, err := linkSelf(summary.RunDir)
	if err != nil {
		return nil, err
	}
	return &run{
		cfg:     cfg,
		q:       queue.New(cfg.Queue),
		opsDir:  opsDir,
		env:     environment(summary, root, cfgPath, binDir),
		log:     log.New(out, "isutori: ", log.Ltime),
		out:     out,
		summary: summary,
	}, nil
}

func environment(s *Summary, root, cfgPath, binDir string) []string {
	return append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"ISUTORI_RUN_ID="+s.RunID,
		"ISUTORI_RUN_DIR="+s.RunDir,
		"ISUTORI_BENCH_RESULT="+filepath.Join(s.RunDir, benchResultFile),
		"ISUTORI_REPO="+root,
		"ISUTORI_CONFIG="+cfgPath,
		"ISUTORI_APP_COMMIT="+s.Entry.AppCommit,
		"ISUTORI_OPS_COMMIT="+s.OpsCommit,
		"ISUTORI_MODE="+s.Entry.Mode,
		"ISUTORI_REQUESTER="+s.Entry.Requester,
	)
}

// Publish retries only the saved publish hook, without queueing, so it never
// holds the servers.
func Publish(ctx context.Context, runDir string, out io.Writer) (*Summary, error) {
	runDir, err := filepath.Abs(runDir)
	if err != nil {
		return nil, err
	}
	root, err := repoRootOf(runDir)
	if err != nil {
		return nil, err
	}
	lock, err := lockRun(runDir)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	b, err := os.ReadFile(filepath.Join(runDir, "run.json"))
	if err != nil {
		return nil, err
	}
	var summary Summary
	if err := json.Unmarshal(b, &summary); err != nil {
		return nil, err
	}
	if summary.RunDir != runDir || summary.RunID != filepath.Base(runDir) {
		return nil, errors.New("run directory does not match run.json; use the original directory")
	}
	result, err := readBenchResult(filepath.Join(runDir, benchResultFile))
	if err != nil {
		return nil, err
	}
	if summary.Bench != nil && *summary.Bench != *result {
		return nil, errors.New("bench.json differs from the saved benchmark result")
	}
	summary.Bench = result
	for i := range summary.Stages {
		if summary.Stages[i].Status == StageRunning {
			summary.Stages[i].Status = StageUnknown
		}
	}
	logFile, err := os.OpenFile(filepath.Join(runDir, "run.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	defer logFile.Close()
	r, err := loadRun(root, &summary, io.MultiWriter(out, logFile))
	if err != nil {
		return nil, err
	}
	if r.cfg.Hooks.Publish == "" {
		return nil, errors.New("no publish hook configured in the saved run")
	}
	return &summary, r.stage(ctx, "publish", r.hook(r.cfg.Hooks.Publish))
}

func lockRun(runDir string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(runDir, "run.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("run is already in use: %w", err)
	}
	return f, nil
}

// Hooks need this executable on PATH when invoked through `go run`.
func linkSelf(runDir string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	binDir := filepath.Join(runDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", err
	}
	// A retried publish may run from another executable than the first run.
	link := filepath.Join(binDir, "isutori")
	if err := os.Remove(link); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return binDir, os.Symlink(self, link)
}

func extract(ctx context.Context, root, commit, dest string) error {
	tarball := dest + ".tar"
	if _, err := git(ctx, root, "archive", "-o", tarball, commit); err != nil {
		return err
	}
	if out, err := exec.CommandContext(ctx, "tar", "-xf", tarball, "-C", dest).CombinedOutput(); err != nil {
		return fmt.Errorf("extract %s: %w: %s", commit, err, out)
	}
	return os.Remove(tarball)
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("git %v: %w: %s", args, err, exitErr.Stderr)
		}
		return "", fmt.Errorf("git %v: %w", args, err)
	}
	return strings.TrimSpace(string(out)), nil
}
