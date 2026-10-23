## デプロイとベンチ (isutori)

- 共有サーバーへのデプロイとベンチ実行は、必ず `isutori bench` を通す。サーバーを直接書き換えたり、ポータルで直接 Enqueue したりしない
- 測りたいコミットは、先に origin へ push しておく。`isutori bench --ref <ブランチ>` で、HEAD 以外も測れる
- 1 人のもとで複数のエージェントが並列に動いているときは、`isutori bench --requester <名前>` で自分を見分けられる名前 (worktree の名前など) を付ける。付けないと全員が `git config user.name` の名前で並び、`isutori status` で区別できない
- `isutori bench` はバックグラウンドで起動し、待っている間は別の作業を進める。結果は stdout の JSON と終了コードで判断する (0 は pass、1 は isutori かフックのエラー、2 はベンチの fail)
- 混み具合は `isutori status --json` で確かめる。前に何件も並んでいるときは、手元で確かめられることを先に済ませる
- フックと `isutori.toml` は、`isutori.toml` の `ops.ref` (既定は origin/main) から取り出したものが使われる。変えたら、そのブランチへ push するまで反映されない
- ベンチ結果を取得した後に回収・分析・共有が失敗したら、ベンチを回し直さず、まずその回の `run.json` を確認する。共有だけを再試行するときは `isutori publish <run_dir>` を使う。保存済みの publish フックが、不足した成果物と再送を扱えること、共有先への書き込みが許可されていることを先に確かめる
