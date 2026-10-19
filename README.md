# isutori

ISUCON で、チームの共有サーバーへのデプロイとベンチ実行を 1 本ずつ順番に回すためのツールです。

キューは `queue.host` (通常 s1) に置き、SSH 越しに操作します。サーバーに常駐プロセスは置きません。

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

## 使い方

競技リポジトリの直下で実行します。

```sh
go run github.com/traP-jp/isutori@latest init     # isutori.toml の雛形と .gitignore への追記
go run github.com/traP-jp/isutori@latest bench    # HEAD をデプロイしてベンチ
go run github.com/traP-jp/isutori@latest status --json
```

| コマンド | 内容 |
|---|---|
| `bench [--ref REF] [--mode measure\|final] [--requester NAME]` | REF (既定 `HEAD`、push 済みであること) をデプロイしてベンチを回す |
| `guard` | 自分のチケットがキューの先頭でなければ 0 以外で終了する。デプロイ手段の冒頭で呼ぶ |
| `receive [--listen ADDR]` | `bench` の段階コマンドから呼び、手で入力された結果を受け取る |
| `autobench [--listen ADDR] PORTAL_URL TARGET` | `bench` の段階コマンドから呼び、ユーザースクリプトにベンチを回させて結果を受け取る |
| `status [--json]` | キューの中身と、最後にデプロイされたものを表示する |
| `queue clear` | キューを空にする。キューが詰まったときの強制解除用 |
| `init` | `isutori.toml` の雛形を書く |
| `docs` | この README を表示する |
| `userscript [MATCH_PATTERN...]` | ユーザースクリプトを書き出す |
| `mock-portal` | 偽ポータルを起動する |

`bench` は進行状況を stderr に、実行の要約 JSON を stdout に出します。終了コードは、ベンチが pass なら 0、途中でエラーなら 1、ベンチが fail なら 2 です。AI に使わせるときは、バックグラウンドで起動して待ち時間に別の作業をさせてください。

順番を待つ間、isutori は 3 秒ごとに `queue.host` へ SSH で接続します。ベンチ中のサーバーに接続確立の負荷をかけないよう、`~/.ssh/config` で接続を使い回す設定にしておくことを勧めます。

```
Host s1
  ControlMaster auto
  ControlPath ~/.ssh/cm-%C
  ControlPersist 60
```

## 測定の流れ

1. REF が origin にあるか確かめ、`ops.ref` (既定 `origin/main`) を取り出す。`isutori.toml` と段階コマンドは、取り出したものを使う
2. キューに並び、先頭になるまで待つ
3. `deploy` → `health` → `prepare` → `bench` → `collect` → `analyze` → `publish` の順に進む
4. `cleanup`。`deploy` に進んだ回は、途中で失敗しても Ctrl-C で止めても呼ぶ

ただし、リースが切れていた回は、サーバーが次の人に渡っている可能性があるため `cleanup` を呼びません。リースの更新が `lease_ttl` の 4 分の 3 のあいだ一度も成功しなかった回も、リースが切れたものとみなして止めます。

Ctrl-C をもう一度押すと、実行中の段階コマンドを止め、`cleanup` を飛ばしてキューを空けてから終了します。3 回目を押すと、何も待たずに終了します。

`status` が表示するデプロイの記録には `state` があります。`deploy` を始めるときに `deploying` として書き、`health` が通ると `healthy` に書き換えます。`health` が未設定なら、`healthy` ではなく `deployed` にします。

## 段階コマンドとの約束事

段階コマンドは `isutori.toml` の `[hooks]` に書き、`ops.ref` から取り出したディレクトリを作業ディレクトリにして、`sh -c` で実行します。0 以外で終了するか、`hooks.timeout` (既定 10 分) を超えると、残りを飛ばして `cleanup` へ進みます。`bench` の上限だけは、ポータルの順番待ちを含むため `hooks.bench_timeout` (既定 20 分) で別に決めます。未設定の段階は飛ばします (`deploy` と `bench` は必須)。段階コマンドの中では `isutori guard` をそのまま呼べます。

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
- `isutori autobench <ポータルの URL> <対象サーバー>`: ポータルをブラウザで開き、ユーザースクリプトに Enqueue させて結果を受け取ります。待っている間も、上と同じフォームから手で入力できます。対象サーバーは、ポータルの選択肢にある番号か IP で書きます。最後に選んだサーバーが最終計測にも使われるポータルがあるため、ユーザースクリプトは一致する選択肢が無いとき、別のサーバーで代用せず、エラーを返します。ブラウザを開くコマンドは `--open-command` で指定でき、未指定なら `wslview`、`xdg-open`、`open` のうち最初に見つかったものを使います
- 自作のスクリプト: ポータルにベンチを回す API などがあれば、それを呼んで結果を書きます

### ユーザースクリプト

`isutori userscript 'https://portal.example.com/*' > isutori.user.js` で書き出し、Tampermonkey に入れます。

当日書き換えるのは、ポータルの画面に依存する先頭の `adapter` だけです。満たすべき約束事と、ベンチを回さずに確かめる方法は、スクリプト内のコメントにあります。同梱の実装は偽ポータル向けです。書き換えた版は競技リポジトリに保存し、全員で同じものを使ってください。

## 偽ポータル

`isutori mock-portal` は、[ISUCON14 のポータル](https://github.com/isucon/isucon14-portal) の競技者画面を偽のデータで動かします。ユーザースクリプトを本番前に試すためのものです。

- ジョブは `--pending` と `--running` の時間をかけて進み、`--outcome` (`pass` / `fail` / `error`) の結果で終わります
- 本物と同じく、実行中に Enqueue すると 422 で断られ、対象サーバーを選ばずに Enqueue すると 404 になります

## 開発

```sh
mise install
go test ./...
testenv/e2e.sh   # Docker の sshd を s1 に見立て、3 人同時の bench で排他と後始末を確かめる
```

skill を変えたら `plugin.json` の `version` を上げます。Codex はこの版ごとに plugin をキャッシュします。

偽ポータルの画面は `mockportal/` でビルドし、成果物を `internal/mockportal/dist/` にコミットしています。isutori を使うだけなら Node は不要です。Dependabot が npm の依存を上げる PR では、CI がビルドし直した成果物が PR に自動でコミットされます。そのコミットに対する CI は承認待ちになるので、PR の画面から承認して実行してください。

```sh
cd mockportal
pnpm install
pnpm exec playwright install chromium   # 初回のみ
pnpm build       # 画面をビルドして dist/ を更新する
pnpm typecheck   # 画面、スクリプト、ユーザースクリプトの型検査
pnpm test        # 偽ポータルに対してユーザースクリプトを動かす
```

## ライセンス

MIT License です。偽ポータルの画面に同梱している第三者のコードのライセンスは、`internal/mockportal/dist/THIRD_PARTY_LICENSES` にあります。
