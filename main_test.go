package main

import (
	"bytes"
	"context"
	"encoding/json"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/traP-jp/isutori/internal/queue"
)

func TestReadmeListsEveryDocPage(t *testing.T) {
	readme, err := readDoc(nil)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := docs.ReadDir("docs")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".md")
		for _, want := range []string{"(docs/" + e.Name() + ")", "`isutori docs " + name + "`"} {
			if !strings.Contains(string(readme), want) {
				t.Errorf("README does not mention %s", want)
			}
		}
		if _, err := readDoc([]string{name}); err != nil {
			t.Errorf("readDoc(%q): %v", name, err)
		}
	}
}

func TestReadDocRejectsUnknownPage(t *testing.T) {
	for _, name := range []string{"missing", "../README", "hooks.md"} {
		if _, err := readDoc([]string{name}); err == nil {
			t.Errorf("readDoc(%q) succeeded", name)
		}
	}
}

func TestStatusShowsRunsThatAreFinishing(t *testing.T) {
	queued := []queue.Entry{{RunID: "next", Requester: "b", Ref: "feature", Mode: "measure", EnqueuedAt: time.Now(), LeaseAge: 5 * time.Second}}
	finishing := []queue.Entry{{RunID: "done", Requester: "a", Ref: "main", Mode: "final", LeaseAge: 90 * time.Second}}

	var table bytes.Buffer
	writeStatusTable(&table, queued, finishing, nil)
	for _, want := range []string{"next", "finishing (the servers are free", "done", "a", "1m30s ago"} {
		if !strings.Contains(table.String(), want) {
			t.Errorf("table lacks %q:\n%s", want, table.String())
		}
	}
	table.Reset()
	writeStatusTable(&table, queued, nil, nil)
	if strings.Contains(table.String(), "finishing") {
		t.Errorf("a finishing section without finishing runs:\n%s", table.String())
	}

	var out bytes.Buffer
	if err := writeStatusJSON(&out, queued, finishing, nil, time.Minute); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Queue []struct {
			RunID string `json:"run_id"`
		}
		Finishing []struct {
			RunID           string  `json:"run_id"`
			ReleasedSeconds float64 `json:"released_seconds"`
		}
		Deployed json.RawMessage
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if len(doc.Queue) != 1 || len(doc.Finishing) != 1 || doc.Finishing[0].RunID != "done" || doc.Finishing[0].ReleasedSeconds != 90 || string(doc.Deployed) != "null" {
		t.Fatalf("unexpected document:\n%s", out.String())
	}
	out.Reset()
	if err := writeStatusJSON(&out, nil, nil, nil, time.Minute); err != nil || !strings.Contains(out.String(), `"finishing": []`) {
		t.Fatalf("an empty list must be [], not null: %v\n%s", err, out.String())
	}
}

func TestFormatVersion(t *testing.T) {
	build := func(version string, settings ...debug.BuildSetting) *debug.BuildInfo {
		return &debug.BuildInfo{Main: debug.Module{Version: version}, Settings: settings}
	}
	for name, tc := range map[string]struct {
		info *debug.BuildInfo
		want string
	}{
		"installed release":  {build("v0.7.0"), "v0.7.0"},
		"clean local build":  {build("(devel)", debug.BuildSetting{Key: "vcs.revision", Value: "0123456789abcdef"}, debug.BuildSetting{Key: "vcs.modified", Value: "false"}), "devel-0123456"},
		"dirty local build":  {build("(devel)", debug.BuildSetting{Key: "vcs.revision", Value: "0123456789abcdef"}, debug.BuildSetting{Key: "vcs.modified", Value: "true"}), "devel-0123456-dirty"},
		"no version control": {build("(devel)"), "devel"},
		"empty version":      {build(""), "devel"},
	} {
		if got := formatVersion(tc.info); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}

func TestVersionCommandNames(t *testing.T) {
	for _, name := range []string{"version", "--version"} {
		if code, err := dispatch(context.Background(), context.Background(), []string{name}); code != 0 || err != nil {
			t.Errorf("%s: got code %d, error %v", name, code, err)
		}
	}
	if code, _ := dispatch(context.Background(), context.Background(), []string{"-version"}); code == 0 {
		t.Error("an unknown name was accepted")
	}
}
