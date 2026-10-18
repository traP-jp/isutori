package queue

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
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

// Local runs scripts on this machine in Dir. Used in tests in place of SSH.
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
	if err := cmd.Run(); err != nil {
		return nil, &ShellError{Err: err, Stderr: strings.TrimSpace(stderr.String())}
	}
	return stdout.Bytes(), nil
}

type ShellError struct {
	Err    error
	Stderr string
}

func (e *ShellError) Error() string {
	return fmt.Sprintf("%v: %s", e.Err, e.Stderr)
}

func (e *ShellError) Unwrap() error { return e.Err }

func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
