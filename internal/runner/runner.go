package runner

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
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
	// Abort, once done, kills the running hook and skips cleanup.
	Abort     context.Context
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
	if _, err := git(ctx, root, "fetch", "--quiet", "--prune", "origin"); err != nil {
		return nil, err
	}
	appCommit, err := git(ctx, root, "rev-parse", "--verify", opts.Ref+"^{commit}")
	if err != nil {
		return nil, err
	}
	if name, err := git(ctx, root, "rev-parse", "--abbrev-ref", opts.Ref); err == nil && name != "" {
		opts.Ref = name
	}
	if remotes, err := git(ctx, root, "branch", "--remotes", "--contains", appCommit, "--list", "origin/*"); err != nil {
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
	err = r.execute(ctx, opts.Abort)
	if err != nil {
		r.summary.Error = err.Error()
	}
	r.writeSummary()
	return r.summary, err
}

var errAborted = errors.New("aborted by a second interrupt")

func (r *run) execute(parent, abort context.Context) error {
	name, err := r.q.Enqueue(parent, r.summary.Ticket)
	if err != nil {
		return err
	}
	r.log.Printf("enqueued %s", name)
	defer func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), r.cfg.Queue.LeaseTTL/4)
		defer cancel()
		if err := r.q.Remove(ctx, name); err != nil {
			r.log.Printf("WARNING: failed to remove ticket %s; run `isutori queue clear` if it blocks others: %v", name, err)
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
	steps := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"deploy", r.deploy},
		{"health", r.hook(h.Health)},
		{"record", r.recordDeployed(deployedState(h.Health))},
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

	// A stage may have failed because the ticket is gone, e.g. guard in deploy;
	// find out now rather than at the next renewal.
	renewCtx, cancelRenew := context.WithTimeout(leaseCtx, r.cfg.Queue.LeaseTTL/4)
	if err := r.q.Renew(renewCtx, name); errors.Is(err, queue.ErrTicketLost) {
		loseLease(err)
	}
	cancelRenew()
	switch cause := context.Cause(leaseCtx); {
	case errors.Is(cause, errAborted):
		return errors.Join(runErr, cause)
	case leaseLost(cause):
		r.log.Print("WARNING: skipping cleanup because the servers may already belong to the next ticket")
		return errors.Join(runErr, cause)
	}
	return errors.Join(runErr, r.stage(leaseCtx, "cleanup", r.hook(h.Cleanup)))
}

// errLeaseUnconfirmed means the queue host could not be reached for long
// enough that it may have pruned the ticket.
var errLeaseUnconfirmed = errors.New("lease renewals kept failing; the queue host may have handed the servers to the next ticket")

func leaseLost(err error) bool {
	return errors.Is(err, queue.ErrTicketLost) || errors.Is(err, errLeaseUnconfirmed)
}

func (r *run) keepLease(ctx context.Context, cancel context.CancelCauseFunc, name string) {
	ttl := r.cfg.Queue.LeaseTTL
	// The host prunes the ticket lease_ttl after the last renewal, which an
	// unreachable host cannot report, so give up a quarter earlier. A timer of
	// its own keeps a renewal that never returns from delaying this.
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
		case errors.Is(err, queue.ErrTicketLost):
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
		timeout := r.cfg.Hooks.Timeout
		ctx, cancel := context.WithTimeoutCause(ctx, timeout, fmt.Errorf("exceeded hooks.timeout (%v)", timeout))
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
}

// deploy records the deploy before starting it, so that a deploy that fails
// halfway still shows up in `isutori status`.
func (r *run) deploy(ctx context.Context) error {
	if err := r.recordDeployed("deploying")(ctx); err != nil {
		return err
	}
	return r.hook(r.cfg.Hooks.Deploy)(ctx)
}

func deployedState(healthHook string) string {
	if healthHook == "" {
		return "deployed"
	}
	return "healthy"
}

func (r *run) recordDeployed(state string) func(context.Context) error {
	return func(ctx context.Context) error {
		t := r.summary.Ticket
		body, err := json.Marshal(map[string]any{
			"state":       state,
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
}

func (r *run) bench(ctx context.Context) error {
	ctx, cancel := context.WithTimeoutCause(ctx, r.cfg.Bench.Timeout, fmt.Errorf("no result within bench.timeout (%v)", r.cfg.Bench.Timeout))
	defer cancel()

	b := r.cfg.Bench
	ln, err := net.Listen("tcp", b.Listen)
	if err != nil {
		return fmt.Errorf("%w (another isutori bench may be running on this machine)", err)
	}
	r.log.Printf("manual input: http://%s/", b.Listen)
	if b.Method == config.MethodUserscript {
		u := b.PortalURL + "#" + url.Values{"autobench": {r.summary.RunID}, "receiver": {"http://" + b.Listen}}.Encode()
		r.log.Printf("opening %s", u)
		if err := openBrowser(b.OpenCommand, u); err != nil {
			r.log.Printf("WARNING: %v; open the URL above yourself", err)
		}
	}
	res, err := receiver.Wait(ctx, ln, r.summary.RunID)
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
