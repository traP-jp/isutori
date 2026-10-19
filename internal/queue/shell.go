package queue

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// Shell runs a POSIX sh script on the host that holds the queue.
type Shell interface {
	Run(ctx context.Context, script string, stdin io.Reader) ([]byte, error)
}

type SSH struct {
	Command []string
	Host    string
}

func (s SSH) Run(ctx context.Context, script string, stdin io.Reader) ([]byte, error) {
	args := append(slices.Clone(s.Command[1:]), "-o", "BatchMode=yes", s.Host, "sh -c "+Quote(script))
	return run(exec.CommandContext(ctx, s.Command[0], args...), stdin)
}

// Local stands in for SSH in tests.
type Local struct {
	Dir string
}

func (l Local) Run(ctx context.Context, script string, stdin io.Reader) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Dir = l.Dir
	return run(cmd, stdin)
}

func run(cmd *exec.Cmd, stdin io.Reader) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdin = stdin
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Without it, a ProxyCommand child holding the pipes keeps Run from
	// returning after the context kills ssh.
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
