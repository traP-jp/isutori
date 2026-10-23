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

func prepareRefs(ctx context.Context, root, runID, ref string, logger *log.Logger) (string, string, string, error) {
	local, err := config.Load(filepath.Join(root, config.FileName))
	if err != nil {
		return "", "", ref, err
	}
	namespace := "refs/isutori/" + runID + "/"
	logger.Print("fetching origin into " + namespace)
	// Explicit destinations alone still allow configured remote-tracking updates.
	if _, err := git(ctx, root, "fetch", "--quiet", "--refmap=", "--no-tags", "--no-prune", "--no-prune-tags", "--no-write-fetch-head", "--no-auto-maintenance", "--no-write-commit-graph", "--recurse-submodules=no", "origin",
		"+refs/heads/*:"+namespace+"heads/*", "+refs/tags/*:"+namespace+"tags/*"); err != nil {
		return "", "", ref, err
	}
	appCommit, err := resolveRef(ctx, root, namespace, ref)
	if err != nil {
		return "", "", ref, err
	}
	heads, err := git(ctx, root, "for-each-ref", "--format=%(refname)", "--contains="+appCommit, namespace+"heads/")
	if err != nil {
		return "", "", ref, err
	}
	if heads == "" {
		return "", "", ref, fmt.Errorf("%s (%s) is not on origin; push it first so the servers can fetch it", ref, appCommit)
	}
	opsCommit, err := resolveRef(ctx, root, namespace, local.Ops.Ref)
	if err != nil {
		return "", "", ref, err
	}
	if name, err := git(ctx, root, "rev-parse", "--abbrev-ref", "--verify", "--end-of-options", ref); err == nil && name != "" {
		ref = name
	}
	logger.Printf("fixed app=%s ops=%s", appCommit, opsCommit)
	return appCommit, opsCommit, ref, nil
}

func resolveRef(ctx context.Context, root, namespace, ref string) (string, error) {
	if ref == "FETCH_HEAD" || strings.HasPrefix(ref, "FETCH_HEAD~") || strings.HasPrefix(ref, "FETCH_HEAD^") {
		return "", fmt.Errorf("%s: use an origin branch or a commit ID instead of FETCH_HEAD", ref)
	}
	base, suffix := ref, ""
	if i := strings.IndexAny(ref, "~^:@"); i >= 0 {
		base, suffix = ref[:i], ref[i:]
	}
	if strings.HasPrefix(suffix, "@{") {
		if end := strings.IndexByte(suffix, '}'); end >= 0 {
			selector := suffix[:end+1]
			if selector == "@{u}" || selector == "@{upstream}" || selector == "@{push}" {
				branch := base
				if branch == "" {
					branch = "HEAD"
				}
				branch, err := git(ctx, root, "rev-parse", "--symbolic-full-name", "--verify", "--end-of-options", branch)
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
				symbolic, err := git(ctx, root, "for-each-ref", "--format=%("+field+")", branch)
				if err != nil {
					return "", err
				}
				if symbolic == "" {
					return "", fmt.Errorf("%s: branch has no %s configured", ref, field)
				}
				return resolveRef(ctx, root, namespace, symbolic+suffix[end+1:])
			}
		}
	}
	remote := ""
	switch {
	case strings.HasPrefix(base, "refs/remotes/origin/"):
		remote = strings.TrimPrefix(base, "refs/remotes/origin/")
	case strings.HasPrefix(base, "remotes/origin/"):
		remote = strings.TrimPrefix(base, "remotes/origin/")
	case strings.HasPrefix(base, "origin/"):
		remote = strings.TrimPrefix(base, "origin/")
	}
	symbolic, _ := git(ctx, root, "rev-parse", "--symbolic-full-name", "--verify", "--end-of-options", base)
	if strings.HasPrefix(symbolic, "refs/remotes/origin/") {
		remote = strings.TrimPrefix(symbolic, "refs/remotes/origin/")
	} else if symbolic != "" {
		remote = ""
	}
	if remote != "" {
		if symbolic == "" {
			if _, err := git(ctx, root, "rev-parse", "--verify", "--end-of-options", base+"^{commit}"); err == nil {
				return "", fmt.Errorf("%s: ambiguous ref; specify refs/heads/... or refs/remotes/origin/...", ref)
			}
		}
		if strings.HasPrefix(suffix, "@{") {
			return "", fmt.Errorf("%s: origin reflogs cannot be resolved from the fetched snapshot; use a commit ID", ref)
		}
		base = namespace + "heads/" + remote
	} else {
		if commit, err := git(ctx, root, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}"); err == nil {
			return commit, nil
		}
		base = namespace + "tags/" + strings.TrimPrefix(strings.TrimPrefix(base, "refs/"), "tags/")
	}
	return git(ctx, root, "rev-parse", "--verify", "--end-of-options", base+suffix+"^{commit}")
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
