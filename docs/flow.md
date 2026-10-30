# 測定の流れ

`isutori bench` を 1 回実行したときに、キューとフックがどの順で進むかを説明します。

`isutori bench` を 1 回実行するごとに、キューに 1 件のエントリを並べます。先頭のエントリの実行だけが、共有サーバーを操作できます。

1. REF が origin にあるか確かめ、コミットを確定する。アプリ、`isutori.toml`、フックは、すべてこのコミットから取り出したものを使う
2. エントリをキューに並べ、先頭になるまで待つ
3. `deploy` → `health` → `prepare` → `bench` → `collect` → `analyze` → `publish` の順に進む
4. `cleanup` を呼ぶ。`deploy` に進んだ実行では、途中で失敗したときや Ctrl-C で中断したときにも呼ぶ

`hooks.release_after` に `collect` を書くと、順番を早く渡せます。`collect` の後に `cleanup` を呼び、キューのエントリを消して、サーバーを次の実行に渡します。`analyze` と `publish` は、その後にキューの外で実行します。順序は `deploy` → `health` → `prepare` → `bench` → `collect` → `cleanup` → (順番を渡す) → `analyze` → `publish` です。

- 順番を渡した後は、リースを延長しません。`analyze` と `publish` は、サーバーを操作しないフックにしてください。
- `cleanup` が失敗しても、順番は渡し、`analyze` と `publish` は動かします。
- `collect` が失敗したときは、これまでと同じく `analyze` を飛ばして `publish` を試します。
- bench が失敗した回、中断された回、リースが切れた回は、順番を渡す前に終わるので、`analyze` と `publish` を動かしません。
- `analyze` と `publish` がどちらも未設定なら、この設定は何もしません。

順番を渡した実行は、`analyze` と `publish` が終わるまで、`status` の `finishing` に載ります。サーバーは空いていますが、その実行の結果はまだ共有されていません。止まった実行の記録は、1 時間を過ぎると消えます。`isutori queue clear` も、この記録を消します。

キューは測るコミットの `isutori.toml` の `[queue]` で決まります。`host` や `dir` を変えたブランチは、ほかのブランチとは別のキューに並び、排他の対象になりません。

エントリにはリース (有効期限) があり、isutori は実行中ずっとリースを延長します。最後の延長から `lease_ttl` (既定 120 秒) が過ぎたエントリは、持ち主の isutori が止まったものとみなして、待っている側が消します。リースが切れていた実行は、サーバーが次の人に渡っている可能性があるため `cleanup` を呼びません。リースの延長が `lease_ttl` の 4 分の 3 の時間続けて失敗した実行も、リースが切れたものとみなして止めます。

Ctrl-C をもう一度押すと、実行中のフックを止め、`cleanup` を飛ばしてキューを空けてから終了します。3 回目を押すと、何も待たずに終了します。

`status` が表示するデプロイの記録には `state` があります。`deploy` を始めるときに `deploying` として書き、`health` が通ると `healthy` に書き換えます。`health` が未設定なら、`healthy` ではなく `deployed` にします。
