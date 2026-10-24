package runner

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/traP-jp/isutori/internal/config"
)

// fixedRefs are the commits a run deploys and takes its hooks from, fixed
// before it joins the queue.
type fixedRefs struct {
	App string
	Ops string
	// Name is the requested ref, abbreviated where git can.
	Name string
}

// prepareRefs fetches origin into a namespace of this run alone, so that
// concurrent runs and the user's own fetches never contend for the same refs.
func prepareRefs(ctx context.Context, root, runID, ref string, logger *log.Logger) (fixedRefs, error) {
	local, err := config.Load(filepath.Join(root, config.FileName))
	if err != nil {
		return fixedRefs{}, err
	}
	namespace := "refs/isutori/" + runID + "/"
	logger.Print("fetching origin into " + namespace)
	// Explicit destinations alone still allow configured remote-tracking updates.
	if _, err := git(ctx, root, "fetch", "--quiet", "--refmap=", "--no-tags", "--no-prune", "--no-prune-tags",
		"--no-write-fetch-head", "--no-auto-maintenance", "--no-write-commit-graph", "--recurse-submodules=no", "origin",
		"+refs/heads/*:"+namespace+"heads/*", "+refs/tags/*:"+namespace+"tags/*"); err != nil {
		return fixedRefs{}, err
	}
	refs := fixedRefs{Name: ref}
	if refs.App, err = resolveRef(ctx, root, namespace, ref); err != nil {
		return fixedRefs{}, err
	}
	heads, err := git(ctx, root, "for-each-ref", "--format=%(refname)", "--contains="+refs.App, namespace+"heads/")
	if err != nil {
		return fixedRefs{}, err
	}
	if heads == "" {
		return fixedRefs{}, fmt.Errorf("%s (%s) is not on origin; push it first so the servers can fetch it", ref, refs.App)
	}
	if refs.Ops, err = resolveRef(ctx, root, namespace, local.Ops.Ref); err != nil {
		return fixedRefs{}, err
	}
	if name, err := git(ctx, root, "rev-parse", "--abbrev-ref", "--verify", "--end-of-options", ref); err == nil && name != "" {
		refs.Name = name
	}
	logger.Printf("fixed app=%s ops=%s", refs.App, refs.Ops)
	return refs, nil
}

// resolveRef resolves ref to a commit. Names of origin branches resolve from
// the snapshot fetched into namespace, and everything else from the local
// repository, falling back to fetched tags.
func resolveRef(ctx context.Context, root, namespace, ref string) (string, error) {
	if ref == "FETCH_HEAD" || strings.HasPrefix(ref, "FETCH_HEAD~") || strings.HasPrefix(ref, "FETCH_HEAD^") {
		return "", fmt.Errorf("%s: use an origin branch or a commit ID instead of FETCH_HEAD", ref)
	}
	base, suffix := splitRevision(ref)
	if selector, rest, ok := upstreamSelector(suffix); ok {
		upstream, err := upstreamOf(ctx, root, ref, base, selector)
		if err != nil {
			return "", err
		}
		return resolveRef(ctx, root, namespace, upstream+rest)
	}
	branch, err := originBranch(ctx, root, ref, base)
	if err != nil {
		return "", err
	}
	if branch != "" {
		if strings.HasPrefix(suffix, "@{") {
			return "", fmt.Errorf("%s: origin reflogs cannot be resolved from the fetched snapshot; use a commit ID", ref)
		}
		return revParseCommit(ctx, root, namespace+"heads/"+branch+suffix)
	}
	if commit, err := revParseCommit(ctx, root, ref); err == nil {
		return commit, nil
	}
	tag := strings.TrimPrefix(strings.TrimPrefix(base, "refs/"), "tags/")
	return revParseCommit(ctx, root, namespace+"tags/"+tag+suffix)
}

func splitRevision(ref string) (base, suffix string) {
	if i := strings.IndexAny(ref, "~^:@"); i >= 0 {
		return ref[:i], ref[i:]
	}
	return ref, ""
}

func upstreamSelector(suffix string) (selector, rest string, ok bool) {
	if !strings.HasPrefix(suffix, "@{") {
		return "", "", false
	}
	end := strings.IndexByte(suffix, '}')
	if end < 0 {
		return "", "", false
	}
	switch selector = suffix[:end+1]; selector {
	case "@{u}", "@{upstream}", "@{push}":
		return selector, suffix[end+1:], true
	}
	return "", "", false
}

// upstreamOf reads the branch configuration, not remote-tracking refs, which
// may be stale or missing.
func upstreamOf(ctx context.Context, root, ref, base, selector string) (string, error) {
	if base == "" {
		base = "HEAD"
	}
	branch, err := git(ctx, root, "rev-parse", "--symbolic-full-name", "--verify", "--end-of-options", base)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(branch, "refs/heads/") {
		return "", fmt.Errorf("%s: specify an unambiguous local branch for %s", ref, selector)
	}
	field := "upstream"
	if selector == "@{push}" {
		field = "push"
	}
	upstream, err := git(ctx, root, "for-each-ref", "--format=%("+field+")", branch)
	if err != nil {
		return "", err
	}
	if upstream == "" {
		return "", fmt.Errorf("%s: branch has no %s configured", ref, field)
	}
	return upstream, nil
}

// originBranch returns the origin branch that base names, or "" when base
// names something local.
func originBranch(ctx context.Context, root, ref, base string) (string, error) {
	symbolic, _ := git(ctx, root, "rev-parse", "--symbolic-full-name", "--verify", "--end-of-options", base)
	if branch, ok := strings.CutPrefix(symbolic, "refs/remotes/origin/"); ok {
		return branch, nil
	}
	if symbolic != "" {
		return "", nil
	}
	var branch string
	for _, prefix := range []string{"refs/remotes/origin/", "remotes/origin/", "origin/"} {
		if b, ok := strings.CutPrefix(base, prefix); ok {
			branch = b
			break
		}
	}
	if branch == "" {
		return "", nil
	}
	if _, err := revParseCommit(ctx, root, base); err == nil {
		return "", fmt.Errorf("%s: ambiguous ref; specify refs/heads/... or refs/remotes/origin/...", ref)
	}
	return branch, nil
}

func revParseCommit(ctx context.Context, root, rev string) (string, error) {
	return git(ctx, root, "rev-parse", "--verify", "--end-of-options", rev+"^{commit}")
}

func cleanupRefs(root, runID string, logger *log.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	refs, err := git(ctx, root, "for-each-ref", "--format=delete %(refname)", "refs/isutori/"+runID+"/")
	if err == nil && refs != "" {
		cmd := exec.CommandContext(ctx, "git", "-C", root, "update-ref", "--stdin")
		cmd.Stdin = strings.NewReader(refs + "\n")
		if output, deleteErr := cmd.CombinedOutput(); deleteErr != nil {
			err = fmt.Errorf("%w: %s", deleteErr, output)
		}
	}
	if err != nil {
		logger.Printf("WARNING: cannot remove temporary refs for %s: %v", runID, err)
	}
}
