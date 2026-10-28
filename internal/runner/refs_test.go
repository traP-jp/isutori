package runner

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func refFixture(t *testing.T) (string, string, string, string) {
	t.Helper()
	dir := t.TempDir()
	origin, work := filepath.Join(dir, "origin"), filepath.Join(dir, "work")
	if err := os.MkdirAll(origin, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, origin, "init", "-b", "main")
	runGit(t, origin, "config", "user.email", "test@example.com")
	runGit(t, origin, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(origin, "isutori.toml"), []byte("[queue]\nhost='test'\n[hooks]\ndeploy='true'\nbench='true'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, origin, "add", ".")
	runGit(t, origin, "commit", "-m", "initial")
	first := runGit(t, origin, "rev-parse", "HEAD")
	runGit(t, origin, "clone", origin, work)
	runGit(t, origin, "commit", "--allow-empty", "-m", "updated ops")
	latest := runGit(t, origin, "rev-parse", "HEAD")
	runGit(t, origin, "tag", "new-tag")
	runGit(t, origin, "branch", "fresh")
	return origin, work, first, latest
}

func runGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	out, err := git(context.Background(), root, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPrepareRefsSnapshot(t *testing.T) {
	_, work, first, latest := refFixture(t)
	logger := log.New(io.Discard, "", 0)
	runGit(t, work, "tag", "local-tag")
	runGit(t, work, "branch", "local-branch")
	runGit(t, work, "branch", "origin/local-branch")
	runGit(t, work, "config", "fetch.prune", "true")
	runGit(t, work, "config", "fetch.pruneTags", "true")
	common := runGit(t, work, "rev-parse", "--absolute-git-dir")
	lock := filepath.Join(common, "refs", "remotes", "origin", "main.lock")
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(lock)
	fetchHead := filepath.Join(common, "FETCH_HEAD")
	if err := os.WriteFile(fetchHead, []byte("unchanged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	refs, err := prepareRefs(context.Background(), work, "snapshot", "HEAD", logger)
	if err != nil {
		t.Fatal(err)
	}
	if refs.Name != "main" {
		t.Fatalf("ref=%q, want main", refs.Name)
	}
	if refs.App != first {
		t.Fatalf("app=%s", refs.App)
	}
	for ref, want := range map[string]string{
		"local-tag": first, "local-branch": first, "origin/local-branch": first,
		"HEAD": first, "HEAD~0": first, first: first,
		"@{upstream}": latest, "HEAD@{u}~1": first,
		"origin": latest, "origin/main": latest, "origin/fresh": latest, "refs/remotes/origin/main~1": first,
		"remotes/origin/main^": first, "origin/HEAD": latest,
		"tags/new-tag": latest, "new-tag": latest, "refs/tags/new-tag^0": latest,
	} {
		got, err := resolveRef(context.Background(), work, "refs/isutori/snapshot/", ref)
		if err != nil || got != want {
			t.Errorf("%s: got %s, %v; want %s", ref, got, err, want)
		}
	}
	if got := runGit(t, work, "rev-parse", "origin/main"); got != first {
		t.Fatalf("tracking ref changed: %s", got)
	}
	if b, err := os.ReadFile(fetchHead); err != nil || string(b) != "unchanged\n" {
		t.Fatalf("FETCH_HEAD changed: %s %v", b, err)
	}
	if got := runGit(t, work, "tag", "--list"); got != "local-tag" {
		t.Fatal("shared tags changed:", got)
	}
	cleanupRefs(work, "snapshot", logger)
	if got := runGit(t, work, "for-each-ref", "refs/isutori/"); got != "" {
		t.Fatal("temporary refs left:", got)
	}
}

func TestPrepareRefsParallelWorktrees(t *testing.T) {
	_, work, first, _ := refFixture(t)
	other := filepath.Join(filepath.Dir(work), "other")
	runGit(t, work, "worktree", "add", "--detach", other, "HEAD")
	logger := log.New(io.Discard, "", 0)
	var wg sync.WaitGroup
	for i, root := range []string{work, other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := []string{"one", "two"}[i]
			defer cleanupRefs(root, id, logger)
			refs, err := prepareRefs(context.Background(), root, id, "HEAD", logger)
			if err != nil || refs.App != first {
				t.Errorf("%s: %s %v", id, refs.App, err)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := git(context.Background(), work, "fetch", "origin"); err != nil {
			t.Error(err)
		}
	}()
	wg.Wait()
	if got := runGit(t, work, "for-each-ref", "refs/isutori/"); got != "" {
		t.Fatal("temporary refs left:", got)
	}
}

func TestPrepareRefsRejectsUnpushedAndDeleted(t *testing.T) {
	_, work, _, _ := refFixture(t)
	runGit(t, work, "config", "user.email", "test@example.com")
	runGit(t, work, "config", "user.name", "test")
	runGit(t, work, "commit", "--allow-empty", "-m", "not pushed")
	runGit(t, work, "update-ref", "refs/remotes/origin/deleted", "HEAD")
	logger := log.New(io.Discard, "", 0)
	defer cleanupRefs(work, "rejected", logger)
	if _, err := prepareRefs(context.Background(), work, "rejected", "HEAD", logger); err == nil || !strings.Contains(err.Error(), "push it first") {
		t.Fatalf("unpushed: %v", err)
	}
	if _, err := resolveRef(context.Background(), work, "refs/isutori/rejected/", "origin/deleted"); err == nil {
		t.Fatal("accepted stale tracking ref")
	}
}

func TestRunLogsPreparationFailure(t *testing.T) {
	_, work, _, _ := refFixture(t)
	runGit(t, work, "remote", "set-url", "origin", filepath.Join(work, "missing"))
	_, err := Run(context.Background(), Options{RepoRoot: work, Ref: "HEAD", Log: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "run.log") {
		t.Fatalf("missing log path: %v", err)
	}
	logs, _ := filepath.Glob(filepath.Join(work, ".isutori", "runs", "*", "run.log"))
	if len(logs) != 1 {
		t.Fatalf("logs: %v", logs)
	}
	b, readErr := os.ReadFile(logs[0])
	if readErr != nil || !strings.Contains(string(b), "FAILED preparation") || !strings.Contains(string(b), "fetch") {
		t.Fatalf("log: %s %v", b, readErr)
	}
}

func TestCleanupRefsKeepsOtherRuns(t *testing.T) {
	_, work, first, _ := refFixture(t)
	runGit(t, work, "update-ref", "refs/isutori/one/heads/main", first)
	runGit(t, work, "update-ref", "refs/isutori/two/heads/main", first)
	cleanupRefs(work, "one", log.New(io.Discard, "", 0))
	if got := runGit(t, work, "for-each-ref", "--format=%(refname)", "refs/isutori/"); got != "refs/isutori/two/heads/main" {
		t.Fatal(got)
	}
}

func TestResolveRefRejectsTransientRefs(t *testing.T) {
	_, work, _, _ := refFixture(t)
	logger := log.New(io.Discard, "", 0)
	defer cleanupRefs(work, "transient", logger)
	if _, err := prepareRefs(context.Background(), work, "transient", "HEAD", logger); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"FETCH_HEAD", "FETCH_HEAD^0", "origin/main@{0}", "origin/main@{1}"} {
		if _, err := resolveRef(context.Background(), work, "refs/isutori/transient/", ref); err == nil {
			t.Errorf("accepted %s", ref)
		}
	}
}

func TestAmbiguousOriginBranch(t *testing.T) {
	_, work, first, _ := refFixture(t)
	runGit(t, work, "branch", "origin/main", first)
	logger := log.New(io.Discard, "", 0)
	defer cleanupRefs(work, "review", logger)
	_, err := prepareRefs(context.Background(), work, "review", "origin/main", logger)
	if err == nil || !strings.Contains(err.Error(), "ambiguous ref") {
		t.Fatalf("expected ambiguous ref error, got %v", err)
	}
	for ref, want := range map[string]string{"refs/heads/origin/main": first, "refs/remotes/origin/main": runGit(t, work, "rev-parse", "refs/isutori/review/heads/main")} {
		got, err := resolveRef(context.Background(), work, "refs/isutori/review/", ref)
		if err != nil || got != want {
			t.Errorf("%s: %s %v, want %s", ref, got, err, want)
		}
	}
}

func TestUpstreamWithoutTrackingRef(t *testing.T) {
	_, work, _, latest := refFixture(t)
	runGit(t, work, "update-ref", "-d", "refs/remotes/origin/main")
	logger := log.New(io.Discard, "", 0)
	defer cleanupRefs(work, "review", logger)
	if _, err := prepareRefs(context.Background(), work, "review", "HEAD@{upstream}", logger); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"HEAD@{upstream}", "@{u}", "main@{u}", "refs/heads/main@{upstream}", "HEAD@{push}"} {
		got, err := resolveRef(context.Background(), work, "refs/isutori/review/", ref)
		if err != nil || got != latest {
			t.Errorf("%s: got=%s error=%v, want %s", ref, got, err, latest)
		}
	}
	runGit(t, work, "config", "--unset", "branch.main.merge")
	if _, err := resolveRef(context.Background(), work, "refs/isutori/review/", "HEAD@{u}"); err == nil {
		t.Fatal("accepted missing upstream")
	}
}
