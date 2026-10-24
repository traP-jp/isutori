# 手動で導入する

isutori を競技リポジトリに手動で組み込む手順です。Claude Code か Codex を使うなら、[README](../README.md#始める) の plugin で、導入をエージェントに任せられます。

## 前提

次の環境を前提にしています。

- 競技リポジトリを Git で管理していて、origin へ push できる
- 手元から各サーバーへ、`ssh s1` のように SSH の別名で入れる
- 手元に mise と Go がある (mise の `go:` バックエンドは Go でビルドする)
- 手元が Linux、macOS、WSL のどれか (Windows で直接は動かない)

## isutori を入れる

チーム全員が同じバージョンを使うよう、競技リポジトリの直下で mise にバージョンを固定して入れ、雛形を作ります。

```sh
mise use --pin go:github.com/traP-jp/isutori@latest
isutori init                # isutori.toml の雛形を書き、.gitignore に .isutori/ を足す
```

## フックを書いて push する

次に、`isutori.toml` の `[hooks]` を埋めます。埋めたら、フックのスクリプトと一緒に commit し、`ops.ref` のブランチ (既定は main) へ push します。isutori はフックと `isutori.toml` を origin から取り出して使うので、push する前の内容は使われません。

フックの書き方は [フックの書き方](hooks.md) に、`bench` のフックで結果を受け取る方法は [ベンチ結果の受け取り](bench-hook.md) にあります。

## SSH の接続を使い回す

順番を待つ間、isutori は 3 秒ごとに `queue.host` へ SSH で接続します。ベンチ中のサーバーの負荷を増やさないよう、`~/.ssh/config` で接続を使い回す設定にしておくことを勧めます。

```
Host s1
  ControlMaster auto
  ControlPath ~/.ssh/cm-%C
  ControlPersist 60
```

## 最初のベンチを回す

```sh
isutori bench               # HEAD をデプロイしてベンチを回す
isutori status --json
```

出力と終了コードの読み方は [使い方](usage.md) にあります。
