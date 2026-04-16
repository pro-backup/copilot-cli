# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

AWS Copilot CLI — a Go CLI tool for building, releasing, and operating containerized applications on AWS ECS (Fargate) and App Runner. End-of-support: June 12, 2026.

## Build & Test Commands

```bash
make build                  # Build local binary → ./bin/local/copilot (packages Node custom resources first)
make run-unit-test          # Go unit tests only → coverage.out
make local-test             # Full test suite: Go + Node unit tests + local integration tests (requires Node.js)
make custom-resource-tests  # Node.js Jest tests for Lambda custom resources
make test-race              # Go tests with -race flag
make gen-mocks              # Regenerate all mockgen mocks (run after changing interfaces)
make integ-test             # Integration tests (WARNING: creates AWS resources, needs default profile)
```

Run a single Go test:
```bash
go test ./internal/pkg/cli/ -run TestSvcDeployOpts
```

## Architecture

### Command Pattern (Cobra-based)
Every CLI command in `internal/pkg/cli/` follows a three-phase pattern:
1. **Validate()** — check flag/argument validity
2. **Ask()** — prompt user for missing required inputs (written to stderr)
3. **Execute()** — perform the action

Commands use "noun verb" grammar: `copilot svc deploy`, `copilot env init`, `copilot app ls`. Entry point: `cmd/copilot/main.go`.

### Key Packages
- **`internal/pkg/cli/`** — All command implementations (~130 files). Each command has a `*Opts` struct with injected dependencies and a corresponding `*_test.go`.
- **`internal/pkg/manifest/`** — YAML manifest parsing. Workloads defined in `copilot/<name>/manifest.yml` are unmarshaled into typed Go structs (LBWebService, BackendService, Job, etc.).
- **`internal/pkg/aws/`** — Thin wrappers around AWS SDK services (~27 subdirs: cloudformation, ecs, ecr, ec2, iam, s3, etc.).
- **`internal/pkg/deploy/cloudformation/`** — Deployment orchestration via CloudFormation stacks.
- **`internal/pkg/template/`** — CloudFormation template rendering. Templates are embedded via `//go:embed` from `templates/` subdirectory.
- **`internal/pkg/config/`** — App/env/workload metadata stored in SSM Parameter Store.
- **`internal/pkg/workspace/`** — Local workspace directory management.
- **`internal/pkg/term/`** — Terminal UI: `selector/` (interactive prompts), `progress/` (spinners), `color/`, `log/`.
- **`cf-custom-resources/`** — Node.js Lambda functions for CloudFormation custom resources.

### Data Flow
```
CLI flags/prompts → Validate → Ask (fill missing) → Parse manifest YAML → 
Render CloudFormation template → Deploy CF stack → Monitor stack events → Output
```

### Application Model
Application → Environments (test, prod) → Services/Jobs (each an ECS task or App Runner service). All infrastructure managed as CloudFormation stacks named `copilot-<app>-<env>-<workload>`.

## Testing Conventions

- **Unit tests**: Colocated as `*_test.go` in same package. No network calls. Use `github.com/golang/mock/mockgen` for mocks, `testify` for assertions.
- **Mocks**: Auto-generated in `mocks/mock_*.go` subdirectories. After changing an interface, run `make gen-mocks`.
- **Integration tests**: Build tag `// +build integration`. Require AWS credentials.
- **Local integration tests**: Build tag `// +build localintegration`.
- **E2E tests**: Ginkgo v2 in `e2e/` directory. Run in Docker, create real AWS resources.
- **Custom resource tests**: Jest in `cf-custom-resources/test/`.

## Style Guide (from STYLE_GUIDE.md)

- Commands: "noun verb" or "verb" only. No nesting beyond one level.
- Flags: lowercase, hyphens for multi-word. Required flags must have short names.
- stdout for data output (tables, JSON); stderr for prompts, progress, and errors.
- Full sentences with capital letters and punctuation for all user-facing text.
- Colors: success=bright green, failure=bright red, user input=cyan, resources=bright cyan, commands=bright cyan with backticks.
- Progress: spinner for <4s ops; spinner with sub-tasks for longer ops.
- Destructive commands must prompt for confirmation; support `-y, --yes` to skip.

## Linting

- Go: golangci-lint with `revive` (exported comment rules, stuttering check disabled). Config: `.golangci.yml`.
- Node: ESLint for custom resources (`cd cf-custom-resources && npm run lint`).
- License headers: `./scripts/license.sh .` — Apache 2.0 required on all source files.

## Dependencies

- Go 1.23 required. New dependencies discouraged — open a PR with proposal first.
- Node.js for custom resources (`cf-custom-resources/`).
- CI validates: build (linux/windows/darwin), mocks are up-to-date, tests pass, license headers present, conventional commit PR titles.
