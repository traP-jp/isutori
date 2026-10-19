// Command isutori serializes deploy-and-benchmark runs of an ISUCON team.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/traP-jp/isutori/internal/config"
	"github.com/traP-jp/isutori/internal/mockportal"
	"github.com/traP-jp/isutori/internal/queue"
	"github.com/traP-jp/isutori/internal/runner"
	"github.com/traP-jp/isutori/internal/userscript"
)

const usage = `usage:
  isutori bench [--ref REF] [--mode measure|final] [--requester NAME]
  isutori guard
  isutori status [--json]
  isutori queue clear
  isutori init
  isutori userscript [MATCH_PATTERN...]
  isutori docs
  isutori mock-portal [--listen ADDR] [--pending D] [--running D] [--outcome pass|fail|error] [--score N]

exit codes of bench: 0 benchmark passed, 1 error, 2 benchmark failed
`

const exitBenchFailed = 2

//go:embed README.md
var readme string

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop()
		fmt.Fprintln(os.Stderr, "isutori: interrupted; press Ctrl-C again to exit immediately")
	}()
	code, err := dispatch(ctx, os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "isutori:", err)
	}
	os.Exit(code)
}

func dispatch(ctx context.Context, args []string) (int, error) {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 1, nil
	}
	switch cmd, rest := args[0], args[1:]; cmd {
	case "bench":
		return bench(ctx, rest)
	case "guard":
		return exitCode(guard(ctx))
	case "status":
		return exitCode(status(ctx, rest))
	case "queue":
		if len(rest) != 1 || rest[0] != "clear" {
			return 1, errors.New("usage: isutori queue clear")
		}
		return exitCode(clearQueue(ctx))
	case "init":
		return exitCode(initRepo(ctx))
	case "docs":
		_, err := os.Stdout.WriteString(readme)
		return exitCode(err)
	case "userscript":
		return exitCode(printUserscript(rest))
	case "mock-portal":
		return exitCode(mockPortal(ctx, rest))
	default:
		fmt.Fprint(os.Stderr, usage)
		return 1, fmt.Errorf("unknown command %q", cmd)
	}
}

func exitCode(err error) (int, error) {
	if err != nil {
		return 1, err
	}
	return 0, nil
}

func bench(ctx context.Context, args []string) (int, error) {
	fs := flag.NewFlagSet("bench", flag.ContinueOnError)
	ref := fs.String("ref", "HEAD", "commit-ish to deploy; must be pushed to origin")
	mode := fs.String("mode", runner.ModeMeasure, "measure or final")
	requester := fs.String("requester", defaultRequester(ctx), "name shown to the team in the queue")
	if err := fs.Parse(args); err != nil {
		return 1, err
	}
	if *mode != runner.ModeMeasure && *mode != runner.ModeFinal {
		return 1, fmt.Errorf("--mode must be %q or %q", runner.ModeMeasure, runner.ModeFinal)
	}
	root, err := repoRoot(ctx)
	if err != nil {
		return 1, err
	}
	summary, err := runner.Run(ctx, runner.Options{
		RepoRoot:  root,
		Ref:       *ref,
		Mode:      *mode,
		Requester: *requester,
		Log:       os.Stderr,
	})
	if summary != nil {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(summary)
	}
	switch {
	case err != nil:
		return 1, err
	case summary.Bench == nil || !summary.Bench.Passed:
		return exitBenchFailed, nil
	}
	return 0, nil
}

func guard(ctx context.Context) error {
	ticket := os.Getenv("ISUTORI_TICKET_ID")
	path := os.Getenv("ISUTORI_CONFIG")
	if ticket == "" || path == "" {
		return errors.New("guard: not running inside `isutori bench`; deploy only through the queue")
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	entries, err := queue.New(cfg.Queue).List(ctx, false)
	if err != nil {
		return err
	}
	if len(entries) == 0 || entries[0].Ticket.ID != ticket {
		return fmt.Errorf("guard: ticket %s is not at the head of the queue", ticket)
	}
	return nil
}

func status(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadRepoConfig(ctx)
	if err != nil {
		return err
	}
	q := queue.New(cfg.Queue)
	entries, err := q.List(ctx, false)
	if err != nil {
		return err
	}
	deployed, err := q.ReadDeployed(ctx)
	if err != nil {
		return err
	}
	if *asJSON {
		type entry struct {
			queue.Ticket
			LeaseAgeSeconds float64 `json:"lease_age_seconds"`
			Expired         bool    `json:"expired"`
		}
		out := struct {
			Queue    []entry         `json:"queue"`
			Deployed json.RawMessage `json:"deployed"`
		}{Queue: []entry{}, Deployed: deployed}
		for _, e := range entries {
			out.Queue = append(out.Queue, entry{e.Ticket, e.LeaseAge.Seconds(), e.LeaseAge > cfg.Queue.LeaseTTL})
		}
		if out.Deployed == nil {
			out.Deployed = json.RawMessage("null")
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "#\tTICKET\tREQUESTER\tREF\tMODE\tWAITING")
	for i, e := range entries {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", i, e.Ticket.ID, e.Ticket.Requester, e.Ticket.Ref, e.Ticket.Mode, time.Since(e.Ticket.EnqueuedAt).Round(time.Second))
	}
	w.Flush()
	if deployed != nil {
		fmt.Printf("\ndeployed: %s\n", strings.TrimSpace(string(deployed)))
	}
	return nil
}

func clearQueue(ctx context.Context) error {
	cfg, err := loadRepoConfig(ctx)
	if err != nil {
		return err
	}
	return queue.New(cfg.Queue).Clear(ctx)
}

func initRepo(ctx context.Context) error {
	root, err := repoRoot(ctx)
	if err != nil {
		return err
	}
	path := filepath.Join(root, config.FileName)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(config.Template); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", path)

	ignore := filepath.Join(root, ".gitignore")
	b, err := os.ReadFile(ignore)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for line := range strings.Lines(string(b)) {
		if strings.TrimSpace(line) == "/.isutori/" {
			return nil
		}
	}
	if len(b) > 0 && !strings.HasSuffix(string(b), "\n") {
		b = append(b, '\n')
	}
	return os.WriteFile(ignore, append(b, "/.isutori/\n"...), 0o644)
}

func printUserscript(match []string) error {
	if len(match) == 0 {
		match = []string{"http://127.0.0.1:7879/*"}
	}
	s, err := userscript.Render(match)
	if err != nil {
		return err
	}
	_, err = os.Stdout.WriteString(s)
	return err
}

func mockPortal(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("mock-portal", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:7879", "address to serve on")
	var c mockportal.Config
	fs.DurationVar(&c.Pending, "pending", 3*time.Second, "time a job stays PENDING")
	fs.DurationVar(&c.Running, "running", 10*time.Second, "time a job stays RUNNING")
	outcome := fs.String("outcome", string(mockportal.Pass), "result of jobs enqueued from now: pass, fail or error")
	fs.Int64Var(&c.Score, "score", 12345, "score of passed jobs")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c.Outcome = mockportal.Outcome(*outcome)
	h, err := mockportal.Handler(c)
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: *listen, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	fmt.Fprintf(os.Stderr, "mock portal: http://%s/contestant\n", *listen)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func loadRepoConfig(ctx context.Context) (*config.Config, error) {
	root, err := repoRoot(ctx)
	if err != nil {
		return nil, err
	}
	return config.Load(filepath.Join(root, config.FileName))
}

func repoRoot(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("not inside a git repository: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func defaultRequester(ctx context.Context) string {
	if out, err := exec.CommandContext(ctx, "git", "config", "user.name").Output(); err == nil {
		if name := strings.TrimSpace(string(out)); name != "" {
			return name
		}
	}
	return os.Getenv("USER")
}
