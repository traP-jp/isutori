## デプロイとベンチ (isutori)

- 共有サーバーへのデプロイとベンチ実行は、必ず `isutori bench` を通す。サーバーを直接書き換えたり、ポータルで直接 Enqueue したりしない
- 測りたいコミットは、先に origin へ push しておく。`isutori bench --ref <ブランチ>` で、HEAD 以外も測れる
- `isutori bench` はバックグラウンドで起動し、待っている間は別の作業を進める。結果は stdout の JSON と終了コードで判断する (0 は pass、1 は isutori か段階コマンドのエラー、2 はベンチの fail)
- 混み具合は `isutori status --json` で確かめる。前に何件も並んでいるときは、手元で確かめられることを先に済ませる
- 段階コマンドと `isutori.toml` は origin/main のものが使われる。変えたら main に push するまで反映されない
