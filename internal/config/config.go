package config

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const FileName = "isutori.toml"

type Config struct {
	Queue Queue `toml:"queue"`
	Ops   Ops   `toml:"ops"`
	Hooks Hooks `toml:"hooks"`
	Bench Bench `toml:"bench"`
}

type Queue struct {
	Host       string        `toml:"host"`
	Dir        string        `toml:"dir"`
	LeaseTTL   time.Duration `toml:"lease_ttl"`
	SSHCommand string        `toml:"ssh_command"`
}

type Ops struct {
	Ref string `toml:"ref"`
}

type Hooks struct {
	Timeout time.Duration `toml:"timeout"`
	Deploy  string        `toml:"deploy"`
	Health  string        `toml:"health"`
	Prepare string        `toml:"prepare"`
	Collect string        `toml:"collect"`
	Analyze string        `toml:"analyze"`
	Publish string        `toml:"publish"`
	Cleanup string        `toml:"cleanup"`
}

const (
	MethodUserscript = "userscript"
	MethodManual     = "manual"
)

type Bench struct {
	Method      string        `toml:"method"`
	PortalURL   string        `toml:"portal_url"`
	Listen      string        `toml:"listen"`
	Timeout     time.Duration `toml:"timeout"`
	OpenCommand string        `toml:"open_command"`
}

func Load(path string) (*Config, error) {
	c := &Config{
		Queue: Queue{Dir: ".isutori", LeaseTTL: 120 * time.Second, SSHCommand: "ssh"},
		Ops:   Ops{Ref: "origin/main"},
		Hooks: Hooks{Timeout: 10 * time.Minute},
		Bench: Bench{Method: MethodManual, Listen: "127.0.0.1:7878", Timeout: 15 * time.Minute},
	}
	md, err := toml.DecodeFile(path, c)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", path, err)
	}
	if keys := md.Undecoded(); len(keys) > 0 {
		return nil, fmt.Errorf("load %s: unknown keys %v", path, keys)
	}
	// The decoder reads an integer duration as nanoseconds.
	for _, key := range []string{"queue.lease_ttl", "hooks.timeout", "bench.timeout"} {
		if md.Type(strings.Split(key, ".")...) == "Integer" {
			return nil, fmt.Errorf("load %s: %s must be a string such as \"10m\"", path, key)
		}
	}
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("load %s: %w", path, err)
	}
	return c, nil
}

func (c *Config) validate() error {
	var errs []string
	if c.Queue.Host == "" {
		errs = append(errs, "queue.host is required")
	}
	if c.Queue.LeaseTTL < 10*time.Second {
		errs = append(errs, "queue.lease_ttl must be at least 10s")
	}
	if c.Hooks.Timeout <= 0 {
		errs = append(errs, "hooks.timeout must be positive")
	}
	if c.Hooks.Deploy == "" {
		errs = append(errs, "hooks.deploy is required")
	}
	switch c.Bench.Method {
	case MethodManual:
	case MethodUserscript:
		if c.Bench.PortalURL == "" {
			errs = append(errs, "bench.portal_url is required when bench.method = \"userscript\"")
		}
	default:
		errs = append(errs, fmt.Sprintf("bench.method must be %q or %q, got %q", MethodUserscript, MethodManual, c.Bench.Method))
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

const Template = `[queue]
host = "s1"           # SSH の接続先。キューの状態はこのホストに置く
# dir = ".isutori"    # 接続先のホームからの相対パス
# lease_ttl = "120s"
# ssh_command = "ssh"

[ops]
ref = "origin/main"   # 段階コマンドと isutori.toml はこの ref から取り出して使う

# 段階コマンドは sh -c で実行する。0 以外で終了したら cleanup へ進む
[hooks]
# timeout = "10m"           # 段階コマンド 1 つあたりの上限
deploy  = "ops/deploy.sh"   # 冒頭で isutori guard を呼ぶこと
health  = "ops/health.sh"
prepare = "ops/prepare.sh"
collect = "ops/collect.sh"
analyze = "ops/analyze.sh"
publish = "ops/publish.sh"
cleanup = "ops/cleanup.sh"  # deploy に進んだ回は、失敗や中断があっても呼ばれる (リースが切れた場合を除く)

[bench]
method = "manual"     # userscript | manual
# portal_url = "https://portal.example.com/"
# listen = "127.0.0.1:7878"
# timeout = "15m"
# open_command = "wslview"
`
