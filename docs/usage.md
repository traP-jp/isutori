# 使い方

導入済みの競技リポジトリで isutori を使うときのコマンドと、うまくいかないときの確かめ方です。

## コマンド

| コマンド | 内容 |
|---|---|
| `bench [--ref REF] [--mode measure\|final] [--requester NAME]` | REF をデプロイしてベンチを回す |
| `publish RUN_DIR` | 保存済みの回の publish フックだけを再実行する |
| `guard` | 自分の実行がキューの先頭でなければ 0 以外で終了する |
| `receive [--listen ADDR]` | `bench` のフックから呼び、手動で入力された結果を受け取る |
| `autobench [OPTIONS] PORTAL_URL TARGET` | `bench` のフックから呼び、ユーザースクリプトにベンチを回させる |
| `inspect [--listen ADDR] [--userscript FILE] PORTAL_URL` | 描画後の DOM と操作履歴を手元に記録する |
| `status [--json]` | キューの中身と、最後にデプロイされたものを表示する |
| `queue clear` | キューを空にする。キューが詰まったときの強制解除用 |
| `init` | `isutori.toml` の雛形を書く |
| `docs [PAGE]` | README か、`docs/` の文書を表示する |
| `help` | コマンドの一覧と使い方を表示する |
| `userscript [MATCH_PATTERN...]` | ユーザースクリプトを書き出す |
| `mock-portal` | 偽ポータルを起動する |

`bench` の REF は既定で `HEAD` です。サーバーが取りに行けるよう、先に origin へ push しておきます。`guard` は、デプロイ手段の冒頭で呼びます。

`bench --mode final` は、競技の終わりに最終計測へ向けた状態をデプロイするときに使います。フックには `ISUTORI_MODE=final` が渡るので、デプロイ手段の中でログの出力やプロファイラを止めてください。ふだんの測定は、既定の `measure` で回します。

`bench` は進行状況を stderr に、実行結果をまとめた JSON を stdout に出します。終了コードは、ベンチが pass なら 0、途中でエラーなら 1、ベンチが fail なら 2 です。エージェントに使わせるときは、バックグラウンドで起動して待ち時間に別の作業をさせてください。

## 計測するコミットと並行実行

複数の worktree から同時に `bench` を実行できます。isutori は origin のブランチとタグを実行ごとに分けて取得し、通常の `origin/*`、ローカルのタグ、`FETCH_HEAD` は更新しません。通常の fetch とも取得先が重ならないため、isutori のために手作業の fetch を止める必要はありません。

`HEAD`、ローカルのブランチとタグ、コミット ID は手元の内容を使います。`origin/main`、`refs/remotes/origin/main`、`origin/main~1` は今回取得した内容から解決します。`HEAD@{upstream}` などが origin のブランチを指す場合も同じです。手元にないタグは今回取得したタグを使います。アプリのコミットが origin のいずれかのブランチに含まれなければ、キューへ登録せず停止します。

`origin/main` などの短い参照名がローカルブランチやタグと重なる場合は、曖昧な指定として停止します。ローカルブランチは `refs/heads/origin/main`、origin のブランチは `refs/remotes/origin/main` のように指定してください。

`--ref` に `FETCH_HEAD` や origin の過去の reflog (`origin/main@{1}` など) は指定できません。取得時点のブランチ名かコミット ID を指定してください。アプリ、`isutori.toml`、フックはすべて同じコミットから使い、そのコミットはキュー登録前に確定します。待機中にブランチが進んでも変更しません。

取得に使った `refs/isutori/<実行 ID>/` の参照は実行終了時に削除します。強制終了や電源断では残る場合がありますが、次の実行は別の参照を使います。isutori は共有の参照を prune しないため、不要になったリモート追跡参照の整理は通常の Git 操作で行ってください。

## うまくいかないとき

- `isutori bench` が失敗したら、その実行の `run.log` で、どのステップで何が出力されたかを確かめます。場所は、stdout に出る JSON の `run_dir` にあります
- fetch や REF の解決に失敗したときは、エラーメッセージに表示される `run.log` を開きます。この段階ではキュー登録前なので、`run.json` はまだありません
- ベンチ結果を取得した後に `collect`、`analyze`、`publish` が失敗したときは、[共有だけをやり直せます](publish.md#保存済みのベンチ結果を共有し直す)
- `isutori.toml` やフックを変えたのに反映されないときは、変更を commit して、測るブランチへ push したかを確かめます
- `autobench` の結果待ちで、ポータルのタブが応答していないという警告が出たときは、ポータルのタブを前面に出します。タブを操作できないときは、受信側の手動入力のページから結果を入力します
- 順番が回ってこないときは、`isutori status` で先頭のエントリを確かめます。持ち主の isutori が止まっていれば、リースが切れた時点でエントリは自動で消えます
- それでもキューが詰まったままなら、チームに知らせてから `isutori queue clear` で空にします
