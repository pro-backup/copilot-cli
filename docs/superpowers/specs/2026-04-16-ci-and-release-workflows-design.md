# CI & Release Workflows for pro-backup/copilot-cli Fork

**Date:** 2026-04-16
**Status:** Approved

## Context

This is a fork of AWS Copilot CLI (deprecated, EOL June 2026). The fork uses its own version line starting at `v2.0.0`. The existing CI/CD infrastructure (7 GitHub Actions workflows + AWS CodeBuild buildspecs) is tailored to the upstream AWS org and is not usable for this fork.

## Goals

1. Run tests and checks on PRs and pushes to `mainline`
2. Build cross-platform binaries and publish GitHub Releases on tag push
3. Remove all unused CI/CD files

## Files to Remove

### GitHub Actions workflows (`.github/workflows/`)
| File | Reason |
|------|--------|
| `ci.yml` | Replaced by new `ci.yml` |
| `commit.yml` | Merged into new `ci.yml` |
| `homebrew.yml` | AWS-specific homebrew tap (`aws/homebrew-tap`) |
| `stale.yml` | Upstream issue management |
| `doc_builder.yml` | Upstream docs publishing |
| `ci_size_computer.yml` | Compares against upstream `aws/copilot-cli` releases |
| `ci_size_writer.yml` | Companion to size computer |

### AWS CodeBuild buildspecs (`.release/`)
| File | Reason |
|------|--------|
| `buildspec.yml` | AWS CodeBuild, not GitHub Actions |
| `buildspec_e2e.yml` | Same |
| `buildspec_integ.yml` | Same |
| `buildspec_regression.yml` | Same |
| `buildspec_sign.yml` | AWS code signing pipeline |
| `buildspec_stage.yml` | AWS S3 staging pipeline |
| `amazon-ecs-public-key.gpg` | Used by buildspec_sign only |

## New Workflow 1: `.github/workflows/ci.yml`

### Triggers
- Pull request to `mainline` (opened, synchronize, reopened)
- Push to `mainline`
- Tag push (`v*`)

### Jobs

All jobs run on `ubuntu-latest` with Go 1.23 and Node.js 24.x.

#### `test`
Runs `make local-test` (Go unit tests with race detector + local integration tests + Node.js custom resource tests).

#### `mocks`
Runs `make gen-mocks` and verifies no diff in generated files. Ensures interfaces and mocks stay in sync.

#### `build`
Matrix strategy: `[compile-linux, compile-windows, compile-darwin]`. Verifies the project compiles for all platforms. Each matrix entry runs `make package-custom-resources`, `make <target>`, `make package-custom-resources-clean`.

#### `license`
Runs `./scripts/license.sh .` to verify Apache 2.0 license headers on all source files.

### Dropped from upstream ci.yml
- **conventional-commits** job: was validating PR titles for upstream conventions. Not needed.
- **CodeCov uploads**: no CodeCov account configured for this fork.

## New Workflow 2: `.github/workflows/release.yml`

### Trigger
- Tag push matching `v*`

### Jobs

#### `test`
Same as CI test job. Acts as a release gate — binaries are not published if tests fail.

#### `release` (needs: test)
1. Set up Go 1.23 + Node.js 24.x
2. Check out code
3. Run `make release` (builds all 5 binaries via `compile-darwin`, `compile-linux`, `compile-windows`)
4. Extract version from git tag (`${GITHUB_REF#refs/tags/}`)
5. Use `softprops/action-gh-release@v2` to create a GitHub Release with these assets:

| Binary | OS/Arch |
|--------|---------|
| `copilot-darwin-amd64` | macOS Intel |
| `copilot-darwin-arm64` | macOS Apple Silicon |
| `copilot-linux-amd64` | Linux x86_64 |
| `copilot-linux-arm64` | Linux ARM64 |
| `copilot.exe` | Windows x86 |

All binaries cross-compile with `CGO_ENABLED=0` on a single `ubuntu-latest` runner. No macOS runner needed.

### Permissions
The release job needs `contents: write` to create releases and upload assets.

## Release Process

```bash
git tag v2.0.0
git push origin v2.0.0
```

This triggers:
1. `ci.yml` — runs all checks (test, mocks, build, license)
2. `release.yml` — runs tests, then builds and publishes a GitHub Release

Users download binaries from:
```
https://github.com/pro-backup/copilot-cli/releases/download/v2.0.0/copilot-darwin-arm64
```

## Version Injection

No code changes needed. The existing Makefile mechanism works:
```makefile
VERSION=$(shell git describe --always --tags | sed 's/-/+/')
LINKER_FLAGS=-X github.com/aws/copilot-cli/internal/pkg/version.Version=${VERSION}
```

When built from tag `v2.0.0`, the binary reports `copilot version: v2.0.0`.

## Action Versions

Use current stable versions:
- `actions/checkout@v4`
- `actions/setup-go@v5`
- `actions/setup-node@v4`
- `softprops/action-gh-release@v2`
