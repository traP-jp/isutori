---
name: setup-isutori
description: ISUCON の競技リポジトリに isutori (デプロイとベンチ実行のキュー) を組み込む。isutori.toml の作成、既存のデプロイ手段や集計スクリプトの段階コマンドへの結び付け、ユーザースクリプトのポータル対応、エージェント向けの運用ルールの追記、最初のベンチでの確認までを行う。
disable-model-invocation: true
argument-hint: "[portal-url]"
---

# isutori を競技リポジトリに組み込む

isutori が受け持つのはキューと、測定枠の中の進行だけである。デプロイ手段 (ansible、Makefile、シェルなど) や集計スクリプトの中身は作らない。段階に結び付けるものが競技リポジトリに無ければ、作業を止めて利用者に伝える。

次の操作は、実行する前に利用者の承認を得る。

- `git push` (段階コマンドと `isutori.toml` は origin の `ops.ref` から読まれるため、反映には push が要る)
- `isutori bench` (共有サーバーにデプロイし直し、チームの測定枠を数分占有する)

## isutori を入れる

チーム全員が同じ版を使うよう、競技リポジトリの mise に版を固定して入れる。

```sh
mise use --pin go:github.com/traP-jp/isutori@latest
```

mise が使えない場合は、`go run github.com/traP-jp/isutori@<コミット>` を Makefile などで包み、版を固定する。

入れたら、以降の作業に進む前に `isutori docs` で仕様 (コマンド、測定の流れ、段階コマンドとの約束事、環境変数、終了コード) を読む。

## 現状を確かめる

- 競技リポジトリに `origin` があり、push できること
- `ssh s1 true` のように、各サーバーへ SSH の別名で入れること。キューを置くホスト (通常 s1) の別名を控える
- 既存のデプロイ手段と集計スクリプトを読み、`deploy` / `health` / `prepare` / `collect` / `analyze` / `publish` / `cleanup` のどれに当たるかを対応づける

## isutori.toml を書く

`isutori init` で雛形を作り、次の点に気をつけて埋める。

- 段階コマンドは、利用者の作業ツリーではなく `ops.ref` から取り出したディレクトリで実行される。そのため、デプロイするアプリの版は作業ツリーから取らず、`$ISUTORI_APP_COMMIT` で受け取ってそのコミットをサーバーへ載せる
- デプロイ手段そのものの冒頭に `isutori guard` を入れる (playbook の最初のタスク、Makefile のターゲットの 1 行目など)。`hooks.deploy` にだけ書くと、isutori を通さない直接のデプロイを止められない
- `$ISUTORI_MODE` をデプロイ手段に渡し、`final` のときはログ出力やプロファイラを切らせる
- `collect` / `analyze` / `publish` の出力は `$ISUTORI_RUN_DIR` の下に置く
- `cleanup` は、どの段階で失敗した後に呼ばれても動くようにする
- `bench.method` は、ポータルの URL が分かっていれば `userscript`、分からなければ `manual` にする
- `userscript` のときは、ベンチの対象サーバーを利用者に確かめて `bench.target` に書く。最後に選んだサーバーが最終計測にも使われることがあるので、自分で推測して決めない

## ユーザースクリプトを当日のポータルに合わせる

`bench.method = "userscript"` のときに行う。

1. 当日マニュアルとポータルの注意書きに、ポータルの自動操作を禁じる記述が無いかを利用者に確かめてもらう。禁じられていれば `manual` にして、この節を飛ばす
2. `isutori userscript '<ポータルの URL>/*' > ops/isutori.user.js` で書き出す。ポータルの URL は、利用者の依頼に含まれていればそれを使い、無ければ尋ねる
3. 利用者に、ポータルのダッシュボードと、終わったジョブの詳細ページの HTML を保存して渡してもらう。ポータルにはログインが要るので、自分では取りに行かない
4. HTML を読み、スクリプト先頭の `adapter` だけを書き換える。約束事はスクリプト内の JSDoc にある。型注釈は消さない
5. 利用者に Tampermonkey へ入れてもらい、終わったジョブのページでメニューの「isutori: read this page」を実行してもらう。表示された結果がポータルの表示と合うまで `adapter` を直す
6. WSL から使う場合は、Windows のブラウザから `http://127.0.0.1:7878/` に届くことも確かめてもらう。isutori はベンチの段階でしか待ち受けないので、この確認は[確かめる](#確かめる)の節で最初のベンチを回すときに行う

## エージェント向けの運用ルールを追記する

チームの Claude Code と Codex の両方に届くよう、競技リポジトリの `AGENTS.md` に [agent-rules.md](agent-rules.md) の内容を追記する。`CLAUDE.md` には `@AGENTS.md` の行を入れて読み込ませる。既に同じ内容があれば重複させない。

## 反映する

`isutori.toml`、段階コマンド、`ops/isutori.user.js`、`AGENTS.md`、`CLAUDE.md` の変更をコミットし、利用者の承認を得て `ops.ref` のブランチ (既定は main) へ push する。

## 確かめる

1. `isutori status` が通り、キューのホストに SSH で入れること
2. 利用者の承認を得て、`isutori bench` をバックグラウンドで実行する
3. 終了コードが 0 であること、`$ISUTORI_RUN_DIR` の `run.json` と `bench.json` にスコアが入っていること、`collect` 以降の段階の出力が想定どおりであることを確かめる

失敗したら、`run.log` で失敗した段階を特定して直し、[反映する](#反映する)からやり直す。
