# isutori の開発

## テスト

```sh
mise install
go test ./...
testenv/e2e.sh   # Docker の sshd を s1 に見立て、3 人同時の bench で排他と後始末を確かめる
```

## plugin のバージョン

skill を変えたら `plugin.json` の `version` を上げます。Codex はこのバージョンごとに plugin をキャッシュします。

## 偽ポータルの画面

偽ポータルの画面は `mockportal/` でビルドし、成果物を `internal/mockportal/dist/` にコミットしています。isutori を使うだけなら Node は不要です。

```sh
cd mockportal
pnpm install
pnpm exec playwright install chromium   # 初回のみ
pnpm build       # 画面をビルドして dist/ を更新する
pnpm lint        # Biome で書式と lint を検査する。対象にユーザースクリプトを含む
pnpm format      # Biome で書式を整え、直せる lint の指摘を直す
pnpm typecheck   # 画面、スクリプト、ユーザースクリプトの型検査
pnpm test:unit   # ブラウザを起動せず、登録前の準備と状態の遷移を確かめる
pnpm test        # 上の検査と、偽ポータルに対するブラウザでの検査を行う
```

Dependabot が npm の依存を上げる PR では、CI がビルドし直した成果物を PR に自動でコミットします。そのコミットに対する CI は承認待ちになるので、PR の画面から承認して実行してください。

## 文書

README は初めて見た人への案内と文書の索引にとどめ、詳しい説明は `docs/` に置きます。`docs/` の文書はバイナリに埋め込まれ、`isutori docs <名前>` で表示されます。文書を足したら、README の文書の表に載せてください。載せ忘れはテストで見つかります。
