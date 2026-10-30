package queue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/traP-jp/isutori/internal/config"
)

var ErrEntryLost = errors.New("queue entry no longer exists (expired or cleared)")

// Entry's Name, LeaseAge and Waited are set by List and are not stored in the entry's file.
type Entry struct {
	Name     string        `json:"-"`
	LeaseAge time.Duration `json:"-"`
	// Waited is the time since the entry joined, counted on the queue host's
	// clock. EnqueuedAt comes from the requester's clock, which may differ.
	Waited     time.Duration `json:"-"`
	RunID      string        `json:"run_id"`
	Requester  string        `json:"requester"`
	Ref        string        `json:"ref"`
	AppCommit  string        `json:"app_commit"`
	Mode       string        `json:"mode"`
	EnqueuedAt time.Time     `json:"enqueued_at"`
}

// Queue holds one file per entry under Dir/queue. Names start with a host
// counter, so lexical order is arrival order even if a clock steps back. An
// entry's mtime is its lease.
type Queue struct {
	Shell Shell
	Dir   string
	TTL   time.Duration
}

const exitLost = 3

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9_]`)

func (q *Queue) prelude() string {
	return fmt.Sprintf("set -e\nD=%s\nQ=\"$D/queue\"\nmkdir -p \"$Q\"\nexec 9>\"$D/lock\"\nflock 9\n", Quote(q.Dir))
}

func (q *Queue) Enqueue(ctx context.Context, e Entry) (string, error) {
	body, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	suffix := unsafeName.ReplaceAllString(e.Requester, "_") + "-" + e.RunID + ".json"
	// One past the larger of the counter and the clock never goes back, and
	// keeps pace with entries that earlier versions named by the clock.
	script := q.prelude() + fmt.Sprintf(`s=$(cat "$D/seq" 2>/dev/null) || true
now=$(date +%%s%%N)
if [ "$now" -gt "${s:-0}" ]; then s=$now; fi
s=$((s + 1))
echo "$s" > "$D/seq.tmp"
mv "$D/seq.tmp" "$D/seq"
n="$s-"%s
cat > "$Q/.$n.tmp"
mv "$Q/.$n.tmp" "$Q/$n"
printf %%s "$n"
`, Quote(suffix))
	out, err := q.Shell.Run(ctx, script, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("enqueue: %w", err)
	}
	return string(out), nil
}

func (q *Queue) Renew(ctx context.Context, name string) error {
	script := q.prelude() + fmt.Sprintf("f=\"$Q/\"%s\ntouch -c \"$f\"\n[ -e \"$f\" ] || exit %d\n", Quote(name), exitLost)
	_, err := q.Shell.Run(ctx, script, nil)
	if exitErr := (*exec.ExitError)(nil); errors.As(err, &exitErr) && exitErr.ExitCode() == exitLost {
		return ErrEntryLost
	}
	if err != nil {
		return fmt.Errorf("renew %s: %w", name, err)
	}
	return nil
}

func (q *Queue) Remove(ctx context.Context, name string) error {
	if _, err := q.Shell.Run(ctx, q.prelude()+"rm -f \"$Q/\""+Quote(name)+"\n", nil); err != nil {
		return fmt.Errorf("remove %s: %w", name, err)
	}
	return nil
}

func (q *Queue) Clear(ctx context.Context) error {
	if _, err := q.Shell.Run(ctx, q.prelude()+"rm -f \"$Q\"/*.json \"$D\"/finishing/*.json\n", nil); err != nil {
		return fmt.Errorf("clear: %w", err)
	}
	return nil
}

// List returns the entries in arrival order. With prune, entries whose lease
// has expired are deleted instead of returned.
func (q *Queue) List(ctx context.Context, prune bool) ([]Entry, error) {
	ttl := -1
	if prune {
		ttl = int(q.TTL.Seconds())
	}
	return q.list(ctx, `"$Q"`, ttl)
}

// finishingMaxAge is how long a run may keep running analyze and publish
// after it released its turn. A marker older than this belongs to a run that
// died, and is deleted when listed.
const finishingMaxAge = time.Hour

// Release gives up the run's turn while the run keeps going. The marker it
// leaves is listed by ListFinishing until EndFinishing.
func (q *Queue) Release(ctx context.Context, name string, e Entry) error {
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	script := q.prelude() + fmt.Sprintf(`n=%s
mkdir -p "$D/finishing"
cat > "$D/finishing/.$n.tmp"
mv "$D/finishing/.$n.tmp" "$D/finishing/$n"
rm -f "$Q/$n"
`, Quote(name))
	if _, err := q.Shell.Run(ctx, script, bytes.NewReader(body)); err != nil {
		return fmt.Errorf("release %s: %w", name, err)
	}
	return nil
}

func (q *Queue) EndFinishing(ctx context.Context, name string) error {
	if _, err := q.Shell.Run(ctx, q.prelude()+"rm -f \"$D/finishing/\""+Quote(name)+"\n", nil); err != nil {
		return fmt.Errorf("end finishing %s: %w", name, err)
	}
	return nil
}

// ListFinishing returns the runs that released their turn and have not ended,
// oldest first. LeaseAge is the time since the release.
func (q *Queue) ListFinishing(ctx context.Context) ([]Entry, error) {
	return q.list(ctx, `"$D/finishing"`, int(finishingMaxAge.Seconds()))
}

// list reads the entry files in dir, a shell word. A negative ttl keeps every
// file; otherwise files older than ttl seconds are deleted.
func (q *Queue) list(ctx context.Context, dir string, ttl int) ([]Entry, error) {
	script := q.prelude() + fmt.Sprintf(`now=$(date +%%s)
for f in %s/*.json; do
  [ -e "$f" ] || continue
  age=$((now - $(stat -c %%Y "$f")))
  if [ %d -ge 0 ] && [ "$age" -gt %d ]; then rm -f "$f"; continue; fi
  n=${f##*/}
  seq=${n%%%%-*}
  case "$seq" in ''|*[!0-9]*) waited=0 ;; *) waited=$((now - seq / 1000000000)) ;; esac
  if [ "$waited" -lt 0 ]; then waited=0; fi
  printf '%%s\t%%s\t%%s\t' "$n" "$age" "$waited"
  tr -d '\n' < "$f"
  echo
done
`, dir, ttl, ttl)
	out, err := q.Shell.Run(ctx, script, nil)
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	var entries []Entry
	for line := range strings.Lines(string(out)) {
		fields := strings.SplitN(strings.TrimSuffix(line, "\n"), "\t", 4)
		if len(fields) != 4 {
			return nil, fmt.Errorf("list: malformed line %q", line)
		}
		name, body := fields[0], fields[3]
		age, ageErr := strconv.Atoi(fields[1])
		waited, waitedErr := strconv.Atoi(fields[2])
		if ageErr != nil || waitedErr != nil {
			return nil, fmt.Errorf("list: malformed line %q", line)
		}
		e := Entry{Name: name, LeaseAge: time.Duration(age) * time.Second, Waited: time.Duration(waited) * time.Second}
		if err := json.Unmarshal([]byte(body), &e); err != nil {
			return nil, fmt.Errorf("list: entry %s: %w", name, err)
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

func (q *Queue) WriteDeployed(ctx context.Context, body []byte) error {
	script := q.prelude() + "cat > \"$D/deployed.json.tmp\"\nmv \"$D/deployed.json.tmp\" \"$D/deployed.json\"\n"
	if _, err := q.Shell.Run(ctx, script, bytes.NewReader(body)); err != nil {
		return fmt.Errorf("write deployed.json: %w", err)
	}
	return nil
}

// ReadDeployed returns nil when nothing has been deployed through isutori yet.
func (q *Queue) ReadDeployed(ctx context.Context) (json.RawMessage, error) {
	out, err := q.Shell.Run(ctx, q.prelude()+"cat \"$D/deployed.json\" 2>/dev/null || true\n", nil)
	if err != nil {
		return nil, fmt.Errorf("read deployed.json: %w", err)
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, nil
	}
	return out, nil
}

func New(c config.Queue) *Queue {
	return &Queue{
		Shell: SSH{Command: strings.Fields(c.SSHCommand), Host: c.Host},
		Dir:   c.Dir,
		TTL:   c.LeaseTTL,
	}
}
