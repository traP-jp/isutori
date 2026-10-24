# isutori

ISUCON で、チームの共有サーバーへのデプロイとベンチ実行を 1 回ずつ順番に回すためのツールです。

デプロイやベンチなど、各ステップで実行するコマンド (フック) は、チームが競技リポジトリに用意します。isutori は順番待ちの列 (キュー) を管理し、順番が来たらフックを順に実行します。キューは `queue.host` (通常 s1) に置き、SSH で操作するので、サーバーに常駐プロセスは要りません。

## できること

- 何人が同時に `isutori bench` を実行しても、共有サーバーを操作するのはキューの先頭の 1 件だけです。複数の worktree やエージェントからも、実行をキューに並べられます
- 1 回の実行で、デプロイ、ベンチ、ログの回収、分析、結果の共有までを決まった順に進め、結果を JSON と終了コードで返します
- ベンチの結果は、手入力、ポータルを操作するユーザースクリプト、許可された API のどれかで受け取ります
- 実行中の isutori が止まっても、チケットの有効期限が切れれば次の人に順番が回ります

デプロイ手段や集計スクリプトの中身は、isutori には含まれません。ansible、Makefile、シェルなど、チームが使っているものをフックから呼びます。

## 始める

Claude Code か Codex を使っているなら、plugin を入れて `setup-isutori` を実行すれば、導入はエージェントに任せられます。既存のデプロイ手段や集計スクリプトを isutori から呼べるようにするところから、最初のベンチで確かめるところまで、エージェントが必要なことを尋ねながら進めます。

```sh
# Claude Code
claude plugin install isutori --marketplace traP-jp/isutori
# 競技リポジトリで /isutori:setup-isutori <ポータルの URL> を実行する

# Codex
codex plugin marketplace add traP-jp/isutori
codex plugin add isutori@isutori
# 競技リポジトリで $ から isutori:setup-isutori を選び、ポータルの URL を添えて実行する
```

手動での導入手順は [手動で導入する](docs/setup.md) にあります。

## ふだん使うコマンド

```sh
isutori bench                          # HEAD をデプロイしてベンチを回す
isutori bench --ref origin/feature-x   # push 済みの別のブランチを測る
isutori status                         # キューと、最後にデプロイされたものを見る
```

`bench` の終了コードは、ベンチが pass なら 0、途中でエラーなら 1、ベンチが fail なら 2 です。ほかのコマンドとオプションは [使い方](docs/usage.md) にあります。

## 文書

| 読む場面 | 文書 | 端末で読む |
|---|---|---|
| 手動で導入するとき | [手動で導入する](docs/setup.md) | `isutori docs setup` |
| 導入済みのリポジトリで測るとき | [使い方](docs/usage.md) | `isutori docs usage` |
| 実行が失敗したとき | [うまくいかないとき](docs/usage.md#うまくいかないとき) | `isutori docs usage` |
| 実行の進み方を確かめるとき | [測定の流れ](docs/flow.md) | `isutori docs flow` |
| フックを書くとき | [フックの書き方](docs/hooks.md) | `isutori docs hooks` |
| publish フックを書くとき | [ベンチ結果の共有](docs/publish.md) | `isutori docs publish` |
| 結果の共有をやり直すとき | [ベンチ結果の共有](docs/publish.md#保存済みのベンチ結果を共有し直す) | `isutori docs publish` |
| bench フックを書くとき | [ベンチ結果の受け取り](docs/bench-hook.md) | `isutori docs bench-hook` |
| ユーザースクリプトをポータルに合わせるとき | [ユーザースクリプト](docs/userscript.md) | `isutori docs userscript` |
| isutori 自体を直すとき | [CONTRIBUTING.md](CONTRIBUTING.md) | — |

`isutori docs` を引数なしで実行すると、この README を表示します。端末で読めるのは、入っている isutori と同じバージョンの文書です。

## ライセンス

MIT License です。偽ポータルの画面に同梱している第三者のコードのライセンスは、`internal/mockportal/dist/THIRD_PARTY_LICENSES` にあります。
