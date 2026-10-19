package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func load(t *testing.T, body string) (*Config, error) {
	p := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(p, []byte("[queue]\nhost = \"s1\"\n[hooks]\ndeploy = \"x\"\nbench = \"x\"\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

func TestMissingFileSuggestsInit(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), FileName))
	if err == nil || !strings.Contains(err.Error(), "isutori init") {
		t.Fatalf("got %v", err)
	}
}

func TestDurations(t *testing.T) {
	c, err := load(t, "timeout = \"3m\"\n")
	if err != nil || c.Hooks.Timeout != 3*time.Minute {
		t.Fatalf("got %v, %v", c, err)
	}
	if _, err := load(t, "timeout = 600\n"); err == nil || !strings.Contains(err.Error(), "hooks.timeout") {
		t.Fatalf("an integer timeout was accepted: %v", err)
	}
}
