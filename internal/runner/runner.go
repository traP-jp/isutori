package runner

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/traP-jp/isutori/internal/config"
	"github.com/traP-jp/isutori/internal/queue"
	"github.com/traP-jp/isutori/internal/receiver"
)

const (
	ModeMeasure = "measure"
	ModeFinal   = "final"
)

type Options struct {
	RepoRoot  string
	Ref       string
	Mode      string
	Requester string
	Log       io.Writer
}

type Stage struct {
	Name    string  `json:"name"`
	Seconds float64 `json:"seconds"`
	Error   string  `json:"error,omitempty"`
}

type Summary struct {
	RunID     string           `json:"run_id"`
	RunDir    string           `json:"run_dir"`
	Ticket    queue.Ticket     `json:"ticket"`
	OpsCommit string           `json:"ops_commit"`
	Bench     *receiver.Result `json:"bench,omitempty"`
	Stages    []Stage          `json:"stages"`
	Error     string           `json:"error,omitempty"`
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

// Run deploys opts.Ref and benchmarks it once its ticket reaches the head of
// the queue. The returned Summary is non-nil whenever a run directory exists.
func Run(ctx context.Context, opts Options) (*Summary, error) {
	root := opts.RepoRoot
	if _, err := git(ctx, root, "fetch", "--quiet", "origin"); err != nil {
		return nil, err
	}
	appCommit, err := git(ctx, root, "rev-parse", "--verify", opts.Ref+"^{commit}")
	if err != nil {
		return nil, err
	}
	if name, err := git(ctx, root, "rev-parse", "--abbrev-ref", opts.Ref); err == nil && name != "" {
		opts.Ref = name
	}
	if remotes, err := git(ctx, root, "branch", "--remotes", "--contains", appCommit); err != nil {
		return nil, err
	} else if remotes == "" {
		return nil, fmt.Errorf("%s (%s) is not on origin; push it first so the servers can fetch it", opts.Ref, appCommit)
	}
	local, err := config.Load(filepath.Join(root, config.FileName))
	if err != nil {
		return nil, err
	}
	opsCommit, err := git(ctx, root, "rev-parse", "--verify", local.Ops.Ref+"^{commit}")
	if err != nil {
		return nil, err
	}

	ticketID := rand.Text()[:8]
	runID := time.Now().Format("20060102T150405") + "-" + ticketID
	runDir := filepath.Join(root, ".isutori", "runs", runID)
	opsDir := filepath.Join(runDir, "ops")
	if err := os.MkdirAll(opsDir, 0o755); err != nil {
		return nil, err
	}
	if err := extract(ctx, root, opsCommit, opsDir); err != nil {
		return nil, err
	}
	cfgPath := filepath.Join(opsDir, config.FileName)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("%s at ops ref %s: %w", config.FileName, local.Ops.Ref, err)
	}
	binDir, err := linkSelf(runDir)
	if err != nil {
		return nil, err
	}
	logFile, err := os.Create(filepath.Join(runDir, "run.log"))
	if err != nil {
		return nil, err
	}
	defer logFile.Close()
	out := io.MultiWriter(opts.Log, logFile)

	r := &run{
		cfg:    cfg,
		q:      queue.New(cfg.Queue),
		opsDir: opsDir,
		env: append(os.Environ(),
			"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
			"ISUTORI_RUN_ID="+runID,
			"ISUTORI_RUN_DIR="+runDir,
			"ISUTORI_REPO="+root,
			"ISUTORI_CONFIG="+cfgPath,
			"ISUTORI_APP_COMMIT="+appCommit,
			"ISUTORI_OPS_COMMIT="+opsCommit,
			"ISUTORI_MODE="+opts.Mode,
			"ISUTORI_REQUESTER="+opts.Requester,
			"ISUTORI_TICKET_ID="+ticketID,
		),
		log: log.New(out, "isutori: ", log.Ltime),
		out: out,
		summary: &Summary{
			RunID:  runID,
			RunDir: runDir,
			Ticket: queue.Ticket{
				ID:         ticketID,
				Requester:  opts.Requester,
				Ref:        opts.Ref,
				AppCommit:  appCommit,
				Mode:       opts.Mode,
				EnqueuedAt: time.Now(),
			},
			OpsCommit: opsCommit,
		},
	}
	err = r.execute(ctx)
	if err != nil {
		r.summary.Error = err.Error()
	}
	r.writeSummary()
	return r.summary, err
}

func (r *run) execute(parent context.Context) error {
	name, err := r.q.Enqueue(parent, r.summary.Ticket)
	if err != nil {
		return err
	}
	r.log.Printf("enqueued %s", name)
	defer func() {
		ctx, cancel := detached(parent)
		defer cancel()
		if err := r.q.Remove(ctx, name); err != nil {
			r.log.Printf("WARNING: failed to remove ticket %s; run `isutori queue clear` if it blocks others: %v", name, err)
		}
	}()

	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	go r.keepLease(ctx, cancel, name)

	if err := r.waitHead(ctx, name); err != nil {
		return err
	}

	h := r.cfg.Hooks
	steps := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"deploy", r.hook(h.Deploy)},
		{"health", r.hook(h.Health)},
		{"record", r.recordDeployed},
		{"prepare", r.hook(h.Prepare)},
		{"bench", r.bench},
		{"collect", r.hook(h.Collect)},
		{"analyze", r.hook(h.Analyze)},
		{"publish", r.hook(h.Publish)},
	}
	var runErr error
	for _, s := range steps {
		if runErr = r.stage(ctx, s.name, s.fn); runErr != nil {
			break
		}
	}

	if errors.Is(context.Cause(ctx), queue.ErrTicketLost) {
		r.log.Print("WARNING: skipping cleanup because the servers may already belong to the next ticket")
		return errors.Join(runErr, queue.ErrTicketLost)
	}
	// Cleanup gets its own context so that it still runs after Ctrl-C.
	// keepLease holds the lease until execute returns.
	cleanupCtx, cancelCleanup := detached(parent)
	defer cancelCleanup()
	return errors.Join(runErr, r.stage(cleanupCtx, "cleanup", r.hook(h.Cleanup)))
}

func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
}

func (r *run) keepLease(ctx context.Context, cancel context.CancelCauseFunc, name string) {
	t := time.NewTicker(r.cfg.Queue.LeaseTTL / 4)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		err := r.q.Renew(context.WithoutCancel(ctx), name)
		if ctx.Err() != nil {
			return
		}
		switch {
		case errors.Is(err, queue.ErrTicketLost):
			r.log.Printf("ERROR: %v", err)
			cancel(err)
			return
		case err != nil:
			r.log.Printf("WARNING: lease renewal failed, retrying: %v", err)
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
			return queue.ErrTicketLost
		case 0:
			r.log.Print("reached the head of the queue")
			return nil
		}
		if head := entries[0].Ticket; head.ID != last {
			r.log.Printf("waiting: %d ahead, now running %s by %s", pos, head.Ref, head.Requester)
			last = head.ID
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
	start := time.Now()
	err := fn(ctx)
	s := Stage{Name: name, Seconds: time.Since(start).Seconds()}
	if err != nil {
		s.Error = err.Error()
		err = fmt.Errorf("%s: %w", name, err)
		r.log.Printf("FAILED %v", err)
	}
	r.summary.Stages = append(r.summary.Stages, s)
	r.writeSummary()
	return err
}

func (r *run) hook(cmdline string) func(context.Context) error {
	return func(ctx context.Context) error {
		if cmdline == "" {
			r.log.Print("(no hook configured)")
			return nil
		}
		cmd := exec.CommandContext(ctx, "sh", "-c", cmdline)
		cmd.Dir = r.opsDir
		cmd.Env = r.env
		cmd.Stdout = r.out
		cmd.Stderr = r.out
		// Signal the whole process group so that ansible, ssh and the like
		// spawned by the hook do not outlive it.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
		cmd.WaitDelay = 10 * time.Second
		return cmd.Run()
	}
}

func (r *run) recordDeployed(ctx context.Context) error {
	t := r.summary.Ticket
	body, err := json.Marshal(map[string]any{
		"run_id":      r.summary.RunID,
		"ticket":      t.ID,
		"by":          t.Requester,
		"ref":         t.Ref,
		"app_commit":  t.AppCommit,
		"ops_commit":  r.summary.OpsCommit,
		"mode":        t.Mode,
		"deployed_at": time.Now(),
	})
	if err != nil {
		return err
	}
	return r.q.WriteDeployed(ctx, body)
}

func (r *run) bench(ctx context.Context) error {
	ctx, cancel := context.WithTimeoutCause(ctx, r.cfg.Bench.Timeout, fmt.Errorf("no result within bench.timeout (%v)", r.cfg.Bench.Timeout))
	defer cancel()

	b := r.cfg.Bench
	r.log.Printf("manual input: http://%s/", b.Listen)
	if b.Method == config.MethodUserscript {
		u := b.PortalURL + "#" + url.Values{"autobench": {r.summary.RunID}, "receiver": {"http://" + b.Listen}}.Encode()
		r.log.Printf("opening %s", u)
		if err := openBrowser(b.OpenCommand, u); err != nil {
			r.log.Printf("WARNING: %v; open the URL above yourself", err)
		}
	}
	res, err := receiver.Wait(ctx, b.Listen, r.summary.RunID)
	if err != nil {
		return err
	}
	r.summary.Bench = &res
	if err := writeJSON(filepath.Join(r.summary.RunDir, "bench.json"), res); err != nil {
		return err
	}
	if res.Error != "" {
		return fmt.Errorf("userscript: %s", res.Error)
	}
	r.log.Printf("bench result: passed=%v score=%d", res.Passed, res.Score)
	return nil
}

func (r *run) writeSummary() {
	if err := writeJSON(filepath.Join(r.summary.RunDir, "run.json"), r.summary); err != nil {
		r.log.Printf("WARNING: %v", err)
	}
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func openBrowser(command, u string) error {
	if command == "" {
		for _, c := range []string{"wslview", "xdg-open", "open"} {
			if _, err := exec.LookPath(c); err == nil {
				command = c
				break
			}
		}
	}
	if command == "" {
		return errors.New("no browser opener found; set bench.open_command")
	}
	cmd := exec.Command(command, u)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// linkSelf makes `isutori` on PATH refer to this very binary inside hooks,
// which matters when isutori is invoked through `go run`.
func linkSelf(runDir string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	binDir := filepath.Join(runDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", err
	}
	return binDir, os.Symlink(self, filepath.Join(binDir, "isutori"))
}

func extract(ctx context.Context, root, commit, dest string) error {
	archive := exec.CommandContext(ctx, "git", "-C", root, "archive", "--format=tar", commit)
	untar := exec.CommandContext(ctx, "tar", "-x", "-C", dest)
	pipe, err := archive.StdoutPipe()
	if err != nil {
		return err
	}
	untar.Stdin = pipe
	if err := archive.Start(); err != nil {
		return err
	}
	if out, err := untar.CombinedOutput(); err != nil {
		archive.Wait()
		return fmt.Errorf("extract %s: %w: %s", commit, err, out)
	}
	if err := archive.Wait(); err != nil {
		return fmt.Errorf("git archive %s: %w", commit, err)
	}
	return nil
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
