# isutori

ISUCON で、チームの共有サーバーへのデプロイとベンチ実行を 1 本ずつ順番に回すためのツールです。人が叩いても AI が叩いても同じように動きます。

キューは `queue.host` (通常 s1) 上に 1 チケット 1 ファイルで置き、SSH 越しに操作します。サーバー側に常駐プロセスは置きません。

## 使い方

競技リポジトリの直下で実行します。

```sh
go run github.com/traP-jp/isutori@latest init     # isutori.toml の雛形と .gitignore への追記
go run github.com/traP-jp/isutori@latest bench    # HEAD をデプロイしてベンチ
go run github.com/traP-jp/isutori@latest status --json
```

| コマンド | 内容 |
|---|---|
| `bench [--ref REF] [--mode measure\|final] [--requester NAME]` | REF (既定 `HEAD`、origin に push 済みであること) をキューに並べ、先頭になったら段階コマンドを順に実行する |
| `guard` | 自分のチケットがキューの先頭でなければ 0 以外で終了する。デプロイ手段の冒頭で呼ぶ |
| `status [--json]` | キューの中身と、最後にデプロイされたもの |
| `queue clear` | キューを空にする。止まったチケットの強制解除用 |
| `init` | `isutori.toml` の雛形を書く |
| `userscript [MATCH_PATTERN...]` | ユーザースクリプトを stdout に出す (下記) |
| `mock-portal [--outcome pass\|fail\|error] ...` | 偽ポータルを起動する (下記) |

`bench` は進行状況を stderr に、実行の要約 JSON を stdout に出します。終了コードは、ベンチが pass なら 0、途中でエラーなら 1、ベンチが fail なら 2 です。AI に使わせるときは、バックグラウンドで起動して待ち時間に別の作業をさせてください。

## 測定の流れ

1. 指定 REF が origin にあることを確かめ、`ops.ref` (既定 `origin/main`) の中身を `.isutori/runs/<run_id>/ops/` に取り出す。以後、`isutori.toml` と段階コマンドはここのものを使う
2. キューにチケットを置き、先頭になるまで待つ。待っている間もリースを延長し続ける
3. `deploy` → `health` → (`deployed.json` を記録) → `prepare` → ベンチ → `collect` → `analyze` → `publish`
4. `cleanup`。`deploy` に進んだ回は、途中で失敗しても Ctrl-C で止めても必ず呼ぶ
5. チケットを消す

リースが切れて自分のチケットが消えていた場合は、サーバーがすでに次の人のものになっている可能性があるため、`cleanup` を呼ばずに終了します。

## 段階コマンドとの約束事

段階コマンドは `isutori.toml` の `[hooks]` に書き、`ops/` を作業ディレクトリとして `sh -c` で実行します。0 以外で終了すると、残りを飛ばして `cleanup` へ進みます。未設定の段階は飛ばします (`deploy` だけは必須)。

渡す環境変数:

| 変数 | 内容 |
|---|---|
| `ISUTORI_RUN_ID` / `ISUTORI_RUN_DIR` | この回の ID と作業ディレクトリ |
| `ISUTORI_APP_COMMIT` | デプロイするアプリのコミット |
| `ISUTORI_OPS_COMMIT` | 段階コマンドを取り出したコミット |
| `ISUTORI_MODE` | `measure` か `final` |
| `ISUTORI_REQUESTER` / `ISUTORI_TICKET_ID` | 依頼者とチケット |
| `ISUTORI_REPO` | 手元の競技リポジトリのルート |
| `ISUTORI_CONFIG` | 使っている `isutori.toml` |

`PATH` の先頭には、実行中の isutori 自身を指す `isutori` が入っているため、`isutori guard` はそのまま呼べます。

`$ISUTORI_RUN_DIR` には次のファイルができます。

- `run.json`: チケット、各段階の所要時間、ベンチ結果。段階が終わるたびに更新されるので、`publish` から読めます
- `bench.json`: ベンチ結果
- `run.log`: isutori と段階コマンドの出力

## ベンチ結果の受け取り

ベンチの段階では、`bench.listen` (既定 `127.0.0.1:7878`) で受信サーバーが立ちます。

- `manual`: `http://127.0.0.1:7878/` を開き、ポータルの結果をフォームに貼り付けて送信します
- `userscript`: `portal_url#autobench=<run_id>&receiver=<URL>` をブラウザで開きます (`bench.open_command`、既定は `wslview` / `xdg-open` / `open` のうち見つかったもの)。ユーザースクリプトが Enqueue を押し、結果を `POST /api/result` に送ります

`POST /api/result` は `Content-Type: application/json` で、`{"run_id", "passed", "score", "message"}` を受け取ります。結果を得られなかったときは `{"run_id", "error"}` を送ります。このときは bench の段階がエラーになり、`isutori bench` は 1 で終了します。

### ユーザースクリプト

`isutori userscript 'https://portal.example.com/*' > isutori.user.js` で書き出し、Tampermonkey に入れます。中身は 2 つに分かれています。

- 先頭の `adapter`: ポータルの画面に依存する部分。当日のポータルに合わせて書き換えます。同梱の実装は ISUCON14 のポータル (偽ポータル) 向けの例です
  - `enqueue()`: ジョブを 1 つ始め、それを指す値を返す。ポータルに断られたら例外を投げる
  - `currentJob()`: いま表示しているジョブを指す値を返す。Enqueue でページが再読み込みされたときの再開に使う
  - `readResult(job)`: 実行中なら `null`、終わったら `{passed, score, message}` を返す
- それ以外: URL の印の検出、状態の保存 (sessionStorage)、結果の送信、タイムアウト。ポータルに依存しません

Tampermonkey のメニューの「isutori: read this page」は、いま表示しているジョブを `readResult(null)` で読んで表示します。ベンチを消費せずに `adapter` を確かめられます。当日書き換えた版は競技リポジトリに保存し、全員で同じものを使ってください。

## 偽ポータル

`isutori mock-portal` は、ISUCON14 のポータル ([isucon/isucon14-portal](https://github.com/isucon/isucon14-portal)、MIT License) の競技者画面を、ブラウザ内の偽データで動かします。ユーザースクリプトを本番前に試すためのものです。

- ジョブは `--pending` の間 PENDING、`--running` の間 RUNNING になり、その後 `--outcome` の結果で終わります。ジョブは localStorage に残ります
- 実行中に Enqueue すると、本物と同じく 422 で拒否されます
- 初回は対象サーバーが未選択で、そのまま Enqueue すると 404 になります。これも本物と同じです

画面のビルド成果物は `internal/mockportal/dist/` にコミットしてあるので、使うだけなら Node は不要です。画面を変えるときは `mockportal/` で `pnpm install && pnpm build` します。`pnpm test` は、偽ポータルに対してユーザースクリプトを動かし、送られる結果を確かめます (初回は `pnpm exec playwright install chromium` が必要です)。

## 開発

```sh
mise install
go test ./...
testenv/e2e.sh   # Docker の sshd を s1 に見立て、3 人同時の bench で排他と後始末を確かめる
```
