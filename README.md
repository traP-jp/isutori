# isutori

ISUCON で、チームの共有サーバーへのデプロイとベンチ実行を 1 回ずつ順番に回すためのツールです。

デプロイやベンチなど各段階で実行するコマンド (段階コマンド) は、チームが競技リポジトリに用意します。isutori は、順番待ちの列 (キュー) と、順番が来た回の段階コマンドの実行を受け持ちます。キューは `queue.host` (通常 s1) に置いて SSH 越しに操作するので、サーバーに常駐プロセスは要りません。

## エージェントで導入する

Claude Code か Codex を使っているなら、plugin を入れて `setup-isutori` を実行すれば、導入はエージェントに任せられます。既存のデプロイ手段や集計スクリプトを isutori に結び付けるところから、最初のベンチで確かめるところまで、エージェントが必要なことを尋ねながら進めます。

```sh
# Claude Code
claude plugin install isutori --marketplace traP-jp/isutori
# 競技リポジトリで /isutori:setup-isutori <ポータルの URL> を実行する

# Codex
codex plugin marketplace add traP-jp/isutori
codex plugin add isutori@isutori
# 競技リポジトリで $ から isutori:setup-isutori を選び、ポータルの URL を添えて実行する
```

以降の章は、手で導入するときと、導入した後の動きを知りたいときのための説明です。

## 手で導入する

チーム全員が同じ版を使うよう、競技リポジトリの直下で mise に版を固定して入れます。

```sh
mise use --pin go:github.com/traP-jp/isutori@latest
isutori init                # isutori.toml の雛形を書き、.gitignore に追記する
isutori bench               # HEAD をデプロイしてベンチを回す
isutori status --json
```

| コマンド | 内容 |
|---|---|
| `bench [--ref REF] [--mode measure\|final] [--requester NAME]` | REF をデプロイしてベンチを回す |
| `guard` | 自分の回がキューの先頭でなければ 0 以外で終了する |
| `receive [--listen ADDR]` | `bench` の段階コマンドから呼び、手で入力された結果を受け取る |
| `autobench [--listen ADDR] PORTAL_URL TARGET` | `bench` の段階コマンドから呼び、ユーザースクリプトにベンチを回させる |
| `status [--json]` | キューの中身と、最後にデプロイされたものを表示する |
| `queue clear` | キューを空にする。キューが詰まったときの強制解除用 |
| `init` | `isutori.toml` の雛形を書く |
| `docs` | この README を表示する |
| `userscript [MATCH_PATTERN...]` | ユーザースクリプトを書き出す |
| `mock-portal` | 偽ポータルを起動する |

`bench` の REF は既定で `HEAD` です。サーバーが取りに行けるよう、先に origin へ push しておきます。`guard` は、デプロイ手段の冒頭で呼びます。

`bench` は進行状況を stderr に、実行の要約 JSON を stdout に出します。終了コードは、ベンチが pass なら 0、途中でエラーなら 1、ベンチが fail なら 2 です。エージェントに使わせるときは、バックグラウンドで起動して待ち時間に別の作業をさせてください。

順番を待つ間、isutori は 3 秒ごとに `queue.host` へ SSH で接続します。ベンチ中のサーバーに接続確立の負荷をかけないよう、`~/.ssh/config` で接続を使い回す設定にしておくことを勧めます。

```
Host s1
  ControlMaster auto
  ControlPath ~/.ssh/cm-%C
  ControlPersist 60
```

## 測定の流れ

`isutori bench` を 1 回実行するごとに、キューに 1 枚のチケットを並べます。先頭のチケットを持つ回だけが、共有サーバーを操作できます。

1. REF が origin にあるか確かめ、`ops.ref` (既定 `origin/main`) を取り出す。`isutori.toml` と段階コマンドは、取り出したものを使う
2. チケットをキューに並べ、先頭になるまで待つ
3. `deploy` → `health` → `prepare` → `bench` → `collect` → `analyze` → `publish` の順に進む
4. `cleanup` を呼ぶ。`deploy` に進んだ回は、途中で失敗したときや Ctrl-C で中断したときにも呼ぶ

チケットには、持ち主の isutori がまだ動いていることを示すリースがあります。isutori は実行中ずっとリースを更新し、最後の更新から `lease_ttl` (既定 120 秒) が過ぎたチケットは、待っている側が消します。リースが切れていた回は、サーバーが次の人に渡っている可能性があるため `cleanup` を呼びません。リースの更新が `lease_ttl` の 4 分の 3 の時間続けて失敗した回も、リースが切れたものとみなして止めます。

Ctrl-C をもう一度押すと、実行中の段階コマンドを止め、`cleanup` を飛ばしてキューを空けてから終了します。3 回目を押すと、何も待たずに終了します。

`status` が表示するデプロイの記録には `state` があります。`deploy` を始めるときに `deploying` として書き、`health` が通ると `healthy` に書き換えます。`health` が未設定なら、`healthy` ではなく `deployed` にします。

## 段階コマンドとの約束事

段階コマンドは `isutori.toml` の `[hooks]` に書きます。`deploy` と `bench` は必須で、ほかの段階は未設定なら飛ばします。

isutori は、`ops.ref` から取り出したディレクトリを作業ディレクトリにして、段階コマンドを `sh -c` で実行します。段階コマンドの中では `isutori guard` をそのまま呼べます。

段階コマンドが 0 以外で終了するか、上限の時間を超えると、残りを飛ばして `cleanup` へ進みます。上限は `hooks.timeout` (既定 10 分) です。`bench` だけは、ポータルの順番待ちを含むため `hooks.bench_timeout` (既定 20 分) で別に決めます。

段階コマンドには次の環境変数を渡します。

| 変数 | 内容 |
|---|---|
| `ISUTORI_RUN_ID` / `ISUTORI_RUN_DIR` | この回の ID と作業ディレクトリ |
| `ISUTORI_APP_COMMIT` | デプロイするアプリのコミット |
| `ISUTORI_OPS_COMMIT` | 段階コマンドを取り出したコミット |
| `ISUTORI_MODE` | `measure` か `final` |
| `ISUTORI_REQUESTER` / `ISUTORI_TICKET_ID` | 依頼者とチケット |
| `ISUTORI_REPO` | 手元の競技リポジトリのルート |
| `ISUTORI_CONFIG` | 使っている `isutori.toml` |
| `ISUTORI_BENCH_RESULT` | `bench` の段階コマンドが結果を書くファイル |

`$ISUTORI_RUN_DIR` には次のファイルができます。

- `run.json`: チケット、各段階の所要時間、ベンチ結果。段階が終わるたびに更新されるので、`publish` から読めます
- `bench.json`: `bench` の段階コマンドが書いたベンチ結果
- `run.log`: isutori と段階コマンドの出力

## ベンチ結果の受け取り

`bench` の段階コマンドはベンチを回し、結果を次の形で `$ISUTORI_BENCH_RESULT` に書きます。0 以外で終了したときや、結果を書かずに終了したときは、ベンチの fail ではなくエラー (終了コード 1) になります。

```json
{"passed": true, "score": 12345, "message": "ポータルに表示された内容など"}
```

`bench` には、ポータルに合わせて次のどれかを書きます。

- `isutori receive`: `http://127.0.0.1:7878/` で待ち受けます。ポータルで Enqueue し、終わったら結果をこのページのフォームに入力してください
- `isutori autobench <ポータルの URL> <対象サーバー>`: ポータルをブラウザで開き、ユーザースクリプトに Enqueue させて結果を受け取ります。待っている間も、上と同じフォームから手で入力できます
- 自作のスクリプト: ポータルにベンチを回す API などがあれば、それを呼んで結果を書きます

`autobench` の対象サーバーは、ポータルの選択肢にある番号か IP で書きます。最後に選んだサーバーが最終計測にも使われるポータルがあるため、ユーザースクリプトは一致する選択肢が無いとき、別のサーバーで代用せず、エラーを返します。

`autobench` がブラウザを開くコマンドは `--open-command` で指定できます。未指定なら、`wslview`、`xdg-open`、`open` のうち最初に見つかったものを使います。

### ユーザースクリプト

`isutori userscript 'https://portal.example.com/*' > isutori.user.js` で書き出し、Tampermonkey に入れます。

当日書き換えるのは、ポータルの画面に依存する先頭の `adapter` だけです。満たすべき約束事と、ベンチを回さずに確かめる方法は、スクリプト内のコメントにあります。同梱の実装は偽ポータル向けです。書き換えた版は競技リポジトリに保存し、全員で同じものを使ってください。

## 偽ポータル

`isutori mock-portal` は、[ISUCON14 のポータル](https://github.com/isucon/isucon14-portal) の競技者画面を偽のデータで動かします。ユーザースクリプトを本番前に試すためのものです。

- ジョブは `--pending` と `--running` の時間をかけて進み、`--outcome` (`pass` / `fail` / `error`) の結果で終わります
- 本物と同じく、ジョブの実行中の Enqueue は 422 で断られ、対象サーバーを選ばない Enqueue は 404 になります

## 開発

### テスト

```sh
mise install
go test ./...
testenv/e2e.sh   # Docker の sshd を s1 に見立て、3 人同時の bench で排他と後始末を確かめる
```

### plugin の版

skill を変えたら `plugin.json` の `version` を上げます。Codex はこの版ごとに plugin をキャッシュします。

### 偽ポータルの画面

偽ポータルの画面は `mockportal/` でビルドし、成果物を `internal/mockportal/dist/` にコミットしています。isutori を使うだけなら Node は不要です。

```sh
cd mockportal
pnpm install
pnpm exec playwright install chromium   # 初回のみ
pnpm build       # 画面をビルドして dist/ を更新する
pnpm typecheck   # 画面、スクリプト、ユーザースクリプトの型検査
pnpm test        # 偽ポータルに対してユーザースクリプトを動かす
```

Dependabot が npm の依存を上げる PR では、CI がビルドし直した成果物を PR に自動でコミットします。そのコミットに対する CI は承認待ちになるので、PR の画面から承認して実行してください。

## ライセンス

MIT License です。偽ポータルの画面に同梱している第三者のコードのライセンスは、`internal/mockportal/dist/THIRD_PARTY_LICENSES` にあります。
