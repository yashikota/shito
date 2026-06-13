---
description: Use when building a Go CLI tool as a releasable command binary.
license: MIT
metadata:
    github-path: go-cli-builder
    github-ref: refs/tags/v1.0.2
    github-repo: https://github.com/yashikota/skills
    github-tree-sha: 6b8cabd6393c0a4ff163ddd5653ab872ad9541e0
name: go-cli-builder
---
# Go CLI Builder

Use this skill when building a Go CLI tool as a releasable command binary.

## Project Shape

- Put the CLI entrypoint at the repository root as `main.go`.
- Keep `main.go` thin: configure process-wide logging, call the command package, translate known command errors to exit codes, log unexpected errors, and exit.
- Put command behavior outside `main.go`, typically under packages such as `internal`.
- Prefer standard Go project conventions before adding framework-specific structure.
- Write tests for pure-function parts.
- Use `aqua` as the package manager. Initialize it with `aqua init`, run `aqua g -i suzuki-shunsuke/pinact golangci/golangci-lint`, then run `aqua i`.

## Test/Lint

- Run `go test ./...` after every code change.
- Run `golangci-lint run ./...` and `golangci-lint fmt ./...` after every code change.

## Version Information

Embed the release version through the package-level `Version` variable in `main.go`. Refer to this implementation:

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

Create `.github/workflows/release.yaml` and run GoReleaser when a version tag is pushed.
After creating the workflow, run `pinact run --update -min-age 3` to update to current packages.

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
