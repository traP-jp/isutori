package runner

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/traP-jp/isutori/internal/config"
	"github.com/traP-jp/isutori/internal/queue"
)

type shellFunc func(ctx context.Context) error

func (f shellFunc) Run(ctx context.Context, _ string, _ io.Reader) ([]byte, error) {
	return nil, f(ctx)
}

func TestKeepLease(t *testing.T) {
	const ttl = time.Second
	hung := make(chan struct{})
	t.Cleanup(func() { close(hung) })
	tests := []struct {
		name     string
		renew    shellFunc
		wantLost error
	}{
		{"renewals succeed", func(context.Context) error { return nil }, nil},
		{"host unreachable", func(context.Context) error { return errors.New("connection timed out") }, errLeaseUnconfirmed},
		{"renewal never returns", func(context.Context) error { <-hung; return nil }, errLeaseUnconfirmed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := &run{
				cfg: &config.Config{Queue: config.Queue{LeaseTTL: ttl}},
				q:   &queue.Queue{Shell: tt.renew},
				log: log.New(io.Discard, "", 0),
			}
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			go r.keepLease(ctx, cancel, "ticket")

			select {
			case <-ctx.Done():
				if tt.wantLost == nil {
					t.Fatalf("gave up the lease: %v", context.Cause(ctx))
				}
				if !errors.Is(context.Cause(ctx), tt.wantLost) {
					t.Fatalf("cause %v, want %v", context.Cause(ctx), tt.wantLost)
				}
			case <-time.After(ttl):
				if tt.wantLost != nil {
					t.Fatal("still holding the lease after lease_ttl without a renewal")
				}
			}
		})
	}
}

func TestHookKillsWhatIgnoresSIGTERM(t *testing.T) {
	dir := t.TempDir()
	r := &run{
		cfg:    &config.Config{Hooks: config.Hooks{Timeout: 300 * time.Millisecond}},
		opsDir: dir,
		out:    io.Discard,
		log:    log.New(io.Discard, "", 0),
	}
	err := r.hook(`(trap '' TERM; exec sleep 30) >/dev/null 2>&1 & echo $! > pid; wait`)(context.Background())
	if err == nil {
		t.Fatal("hook outlived hooks.timeout")
	}
	b, err := os.ReadFile(filepath.Join(dir, "pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	time.Sleep(100 * time.Millisecond)
	if err := syscall.Kill(pid, 0); err == nil {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Fatal("a child that ignored SIGTERM survived the hook")
	}
}
