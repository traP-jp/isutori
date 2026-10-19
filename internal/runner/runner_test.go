package runner

import (
	"context"
	"errors"
	"io"
	"log"
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
