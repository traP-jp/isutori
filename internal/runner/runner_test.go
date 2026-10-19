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

type unreachable struct{}

func (unreachable) Run(context.Context, string, io.Reader) ([]byte, error) {
	return nil, errors.New("ssh: connect to host s1: Connection timed out")
}

func TestKeepLeaseGivesUpBeforeTheHostPrunes(t *testing.T) {
	ttl := 400 * time.Millisecond
	r := &run{
		cfg: &config.Config{Queue: config.Queue{LeaseTTL: ttl}},
		q:   &queue.Queue{Shell: unreachable{}},
		log: log.New(io.Discard, "", 0),
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	go r.keepLease(ctx, cancel, "ticket")

	select {
	case <-ctx.Done():
	case <-time.After(ttl):
		t.Fatal("still holding the lease after lease_ttl without a renewal")
	}
	if !errors.Is(context.Cause(ctx), queue.ErrTicketLost) {
		t.Fatalf("cause: %v", context.Cause(ctx))
	}
}
