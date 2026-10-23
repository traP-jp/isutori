package config

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const FileName = "isutori.toml"

type Config struct {
	Queue Queue `toml:"queue"`
	Ops   Ops   `toml:"ops"`
	Hooks Hooks `toml:"hooks"`
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
	Timeout      time.Duration `toml:"timeout"`
	BenchTimeout time.Duration `toml:"bench_timeout"`
	Deploy       string        `toml:"deploy"`
	Health       string        `toml:"health"`
	Prepare      string        `toml:"prepare"`
	Bench        string        `toml:"bench"`
	Collect      string        `toml:"collect"`
	Analyze      string        `toml:"analyze"`
	Publish      string        `toml:"publish"`
	Cleanup      string        `toml:"cleanup"`
}

func Load(path string) (*Config, error) {
	c := &Config{
		Queue: Queue{Dir: ".isutori", LeaseTTL: 120 * time.Second, SSHCommand: "ssh"},
		Ops:   Ops{Ref: "origin/main"},
		Hooks: Hooks{Timeout: 10 * time.Minute, BenchTimeout: 20 * time.Minute},
	}
	md, err := toml.DecodeFile(path, c)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s not found; run `isutori init` to create it", path)
	}
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", path, err)
	}
	if keys := md.Undecoded(); len(keys) > 0 {
		return nil, fmt.Errorf("load %s: unknown keys %v", path, keys)
	}
	// The decoder reads an integer duration as nanoseconds.
	for _, key := range []string{"queue.lease_ttl", "hooks.timeout", "hooks.bench_timeout"} {
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
	if c.Hooks.Timeout <= 0 || c.Hooks.BenchTimeout <= 0 {
		errs = append(errs, "hooks.timeout and hooks.bench_timeout must be positive")
	}
	if c.Hooks.Deploy == "" {
		errs = append(errs, "hooks.deploy is required")
	}
	if c.Hooks.Bench == "" {
		errs = append(errs, "hooks.bench is required")
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
ref = "origin/main"   # フックと isutori.toml はこの ref から取り出して使う

# フックは sh -c で実行する。失敗したら残りを飛ばして cleanup へ進む
# collect/analyze の失敗時は、取得済みの結果を共有するために publish を試す
[hooks]
# timeout = "10m"           # bench 以外のフック 1 つあたりの上限
# bench_timeout = "20m"     # bench の上限。ポータルの順番待ちも含む
deploy  = "ops/deploy.sh"   # 冒頭で isutori guard を呼ぶこと
health  = "ops/health.sh"
prepare = "ops/prepare.sh"
bench   = "isutori receive" # 結果を $ISUTORI_BENCH_RESULT に書く。ユーザースクリプトを使うなら "isutori autobench <ポータルの URL> <対象サーバー>"
collect = "ops/collect.sh"
analyze = "ops/analyze.sh"
publish = "ops/publish.sh"
cleanup = "ops/cleanup.sh"  # deploy に進んだ実行では、失敗や中断があっても呼ばれる (リースが切れた場合を除く)
`
