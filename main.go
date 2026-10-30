// Command isutori serializes deploy-and-benchmark runs of an ISUCON team.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/traP-jp/isutori/internal/config"
	"github.com/traP-jp/isutori/internal/inspect"
	"github.com/traP-jp/isutori/internal/mockportal"
	"github.com/traP-jp/isutori/internal/queue"
	"github.com/traP-jp/isutori/internal/receiver"
	"github.com/traP-jp/isutori/internal/runner"
	"github.com/traP-jp/isutori/internal/userscript"
)

const usage = `usage: isutori <command> [arguments]

  bench [--ref REF] [--mode measure|final] [--requester NAME]
      deploy REF (HEAD by default) and benchmark it when its turn comes
  publish RUN_DIR
      retry sharing a saved benchmark result without deploying or benchmarking
  guard
      fail unless this run is at the head of the queue; call it first in deploy
  receive [--listen ADDR]
      from hooks.bench: wait for a result entered by hand
  autobench [--listen ADDR] [--open-command CMD] [--userscript FILE] PORTAL_URL TARGET
      from hooks.bench: let the userscript run the benchmark against TARGET
  inspect [--listen ADDR] [--userscript FILE] PORTAL_URL
      record portal DOM changes locally while setting up the userscript
  status [--json]
      show the queue and what was deployed last
  queue clear
      empty a stuck queue
  init
      write an isutori.toml template and ignore .isutori/
  userscript [MATCH_PATTERN...]
      print the userscript for the portal
  docs [PAGE]
      print the README, or docs/PAGE.md
  mock-portal [--listen ADDR] [--pending D] [--running D] [--outcome pass|fail|error] [--score N]
      serve a fake portal for trying the userscript

exit codes of bench: 0 benchmark passed, 1 error, 2 benchmark failed
`

const exitBenchFailed = 2

//go:embed README.md docs/*.md
var docs embed.FS

func main() {
	// The first interrupt stops gracefully, the second abandons cleanup, and
	// the third exits on the spot.
	ctx, interrupt := context.WithCancel(context.Background())
	abort, abortNow := context.WithCancel(context.Background())
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigs
		interrupt()
		<-sigs
		abortNow()
		<-sigs
		os.Exit(1)
	}()
	code, err := dispatch(ctx, abort, os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "isutori:", err)
	}
	os.Exit(code)
}

func dispatch(ctx, abort context.Context, args []string) (int, error) {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 1, nil
	}
	switch cmd, rest := args[0], args[1:]; cmd {
	case "help", "-h", "--help":
		fmt.Print(usage)
		return 0, nil
	case "bench":
		return bench(ctx, abort, rest)
	case "publish":
		return exitCode(publish(ctx, rest))
	case "guard":
		return exitCode(guard(ctx))
	case "receive":
		return exitCode(receive(ctx, rest))
	case "autobench":
		return exitCode(autobench(ctx, rest))
	case "inspect":
		return exitCode(inspectPortal(ctx, rest))
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
		if len(rest) > 1 {
			return 1, errors.New("usage: isutori docs [PAGE]")
		}
		page, err := readDoc(rest)
		if err != nil {
			return 1, err
		}
		_, err = os.Stdout.Write(page)
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

func bench(ctx, abort context.Context, args []string) (int, error) {
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
		Abort:     abort,
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

func publish(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: isutori publish RUN_DIR")
	}
	summary, err := runner.Publish(ctx, args[0], os.Stderr)
	if summary != nil {
		err = errors.Join(err, json.NewEncoder(os.Stdout).Encode(summary))
	}
	return err
}

func guard(ctx context.Context) error {
	runID := os.Getenv("ISUTORI_RUN_ID")
	path := os.Getenv("ISUTORI_CONFIG")
	if runID == "" || path == "" {
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
	if len(entries) == 0 || entries[0].RunID != runID {
		return fmt.Errorf("guard: run %s is not at the head of the queue", runID)
	}
	return nil
}

const defaultListen = "127.0.0.1:7878"

func receive(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("receive", flag.ContinueOnError)
	listen := fs.String("listen", defaultListen, "address to receive the result on")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: isutori receive [--listen ADDR]")
	}
	return awaitResult(ctx, *listen, "", func(string) {})
}

func autobench(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("autobench", flag.ContinueOnError)
	listen := fs.String("listen", defaultListen, "address to receive the result on")
	scriptPath := fs.String("userscript", userscript.DefaultPath, "userscript file in the saved ops directory")
	open := fs.String("open-command", "", "command that opens the portal; the first of wslview, xdg-open and open by default")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return errors.New("usage: isutori autobench [--listen ADDR] [--open-command CMD] [--userscript FILE] PORTAL_URL TARGET")
	}
	portal, target := fs.Arg(0), fs.Arg(1)
	var deadline time.Time
	if path := os.Getenv("ISUTORI_CONFIG"); path != "" {
		cfg, err := config.Load(path)
		if err != nil {
			return err
		}
		deadline = time.Now().Add(cfg.Hooks.BenchTimeout)
	}
	return awaitResult(ctx, *listen, *scriptPath, func(runID string) {
		values := url.Values{"autobench_v2": {runID}, "receiver": {"http://" + *listen}, "target": {target}}
		if !deadline.IsZero() {
			values.Set("deadline", strconv.FormatInt(deadline.UnixMilli(), 10))
		}
		u := portal + "#" + values.Encode()
		fmt.Fprintf(os.Stderr, "isutori: opening %s\n", u)
		if err := openBrowser(*open, u); err != nil {
			fmt.Fprintf(os.Stderr, "isutori: WARNING: %v; open the URL above yourself\n", err)
		}
	})
}

// awaitResult receives the result of the current run and writes it to
// $ISUTORI_BENCH_RESULT. listening runs once the receiver is up.
func awaitResult(ctx context.Context, listen, scriptPath string, listening func(runID string)) error {
	runID, out := os.Getenv("ISUTORI_RUN_ID"), os.Getenv("ISUTORI_BENCH_RESULT")
	if runID == "" || out == "" {
		return errors.New("not running as hooks.bench of `isutori bench`")
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("%w (another isutori bench may be running on this machine)", err)
	}
	fmt.Fprintf(os.Stderr, "isutori: waiting for the result; enter it by hand at http://%s/ if needed\n", listen)
	if scriptPath != "" {
		fmt.Fprintln(os.Stderr, "isutori: waiting for a verified userscript; manual input remains available")
	}
	listening(runID)
	res, err := receiver.Wait(ctx, ln, receiver.Options{
		RunID:      runID,
		ScriptPath: scriptPath,
		Log:        os.Stderr,
		Save: func(res receiver.Result) error {
			if res.Error != "" {
				return nil
			}
			return runner.SaveBenchResult(out, runner.BenchResult{Passed: res.Passed, Score: res.Score, Message: res.Message, Userscript: res.Userscript})
		},
	})
	if err != nil {
		return err
	}
	if res.Error != "" {
		return fmt.Errorf("userscript: %s", res.Error)
	}
	return nil
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
		return errors.New("no browser opener found; pass --open-command")
	}
	cmd := exec.Command(command, u)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
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
			queue.Entry
			LeaseAgeSeconds float64 `json:"lease_age_seconds"`
			Expired         bool    `json:"expired"`
		}
		out := struct {
			Queue    []entry         `json:"queue"`
			Deployed json.RawMessage `json:"deployed"`
		}{Queue: []entry{}, Deployed: deployed}
		for _, e := range entries {
			out.Queue = append(out.Queue, entry{e, e.LeaseAge.Seconds(), e.LeaseAge > cfg.Queue.LeaseTTL})
		}
		if out.Deployed == nil {
			out.Deployed = json.RawMessage("null")
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "#\tRUN ID\tREQUESTER\tREF\tMODE\tWAITING")
	for i, e := range entries {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", i, e.RunID, e.Requester, e.Ref, e.Mode, time.Since(e.EnqueuedAt).Round(time.Second))
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
	fmt.Fprintln(os.Stderr, "next: fill in [hooks], commit it with the hook scripts, and push; isutori reads isutori.toml and the hooks from the commit it deploys (HEAD by default)")

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

func readDoc(args []string) ([]byte, error) {
	if len(args) == 0 {
		return docs.ReadFile("README.md")
	}
	page, err := docs.ReadFile("docs/" + args[0] + ".md")
	if err == nil {
		return page, nil
	}
	entries, dirErr := docs.ReadDir("docs")
	if dirErr != nil {
		return nil, dirErr
	}
	var names []string
	for _, e := range entries {
		names = append(names, strings.TrimSuffix(e.Name(), ".md"))
	}
	return nil, fmt.Errorf("no page %q; pages: %s", args[0], strings.Join(names, ", "))
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

func inspectPortal(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	scriptPath := fs.String("userscript", userscript.DefaultPath, "working userscript file to compare against")
	addr := fs.String("listen", "127.0.0.1:7877", "loopback address for portal inspection")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: isutori inspect [--listen ADDR] [--userscript FILE] PORTAL_URL")
	}
	ln, err := inspect.Listen(*addr)
	if err != nil {
		return err
	}
	defer ln.Close()
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	session, err := inspect.New(root, fs.Arg(0), *scriptPath)
	if err != nil {
		return err
	}
	defer session.Close()
	fmt.Fprintf(os.Stderr, "isutori: inspection directory: %s\n", session.Dir)
	fmt.Fprintf(os.Stderr, "isutori: open the portal and choose 'isutori: start recording'; paste this connection:\nhttp://%s/#%s\n", ln.Addr(), session.Token)
	fmt.Fprintln(os.Stderr, "isutori: Ctrl-C stops the receiver; existing recordings are kept")
	return inspect.Serve(ctx, ln, session)
}
