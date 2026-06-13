---
name: go-cli-builder
description: リリース可能な通常のコマンドバイナリとして Go CLI ツールを作るときに使う
license: MIT
---

# Go CLI Builder

リリース可能な通常のコマンドバイナリとして Go CLI ツールを作るときに使う。

## プロジェクト構成

- CLI のエントリーポイントはリポジトリ root の `main.go` に置く。
- `main.go` は薄く保つ。プロセス全体の logging 設定、command package の呼び出し、既知の command error から exit code への変換、想定外エラーの logging と exit だけを担当させる。
- 実際の command の振る舞いは `main.go` の外に置く。 `internal` などの package に分ける。
- framework 固有の構成を足す前に、標準的な Go project convention を優先する。
- 純粋関数部分にはテストを書く。
- パッケージマネージャーには `aqua` を使用する。`aqua init` で初期化し、 `aqua g -i suzuki-shunsuke/pinact golangci/golangci-lint` を行った後 `aqua i` する。

## Test/Lint

- コード変更後は毎回 `go test ./...` を実行する
- コード変更後は毎回 `golangci-lint run ./...` `golangci-lint fmt ./...` を実行する

## バージョン情報

`main.go` の package-level 変数 `Version` に release version を埋め込む。実装は以下を参照。  

```go
var Version string

func getVersion() string {
	if Version != "" {
		return Version
	}

	if info, ok := debug.ReadBuildInfo(); ok {
		if info.Main.Version != "(devel)" {
			return info.Main.Version
		}

		if v, ok := getVCSBuildVersion(info); ok {
			return v
		}
	}

	return "(unset)"
}

func getVCSBuildVersion(info *debug.BuildInfo) (string, bool) {
	var (
		revision string
		dirty    string
	)

	for _, v := range info.Settings {
		switch v.Key {
		case "vcs.revision":
			revision = v.Value
		case "vcs.modified":
			if v.Value == "true" {
				dirty = " (dirty)"
			}
		}
	}

	if revision == "" {
		return "", false
	}

	return revision + dirty, true
}
```

## Release

`.github/workflows/release.yaml` を作り、version tag の push で GoReleaser を実行する。  
作成後 `pinact run --update -min-age 3` を実行し最新のパッケージに更新する。  

```yaml
name: Release

on:
  push:
    tags:
      - "v*"

permissions:
  contents: write

jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@8e8c483db84b4bee98b60c0593521ed34d9990e8 # v6.0.1
        with:
          fetch-depth: 0

      - uses: actions/setup-go@4dc6199c7b1a012772edbd06daecab0f50c9053c # v6.1.0
        with:
          go-version-file: go.mod
          cache: true

      - name: Run GoReleaser
        uses: goreleaser/goreleaser-action@e435ccd777264be153ace6237001ef4d979d3a7a # v6.4.0
        with:
          distribution: goreleaser
          version: '~> v2'
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```
