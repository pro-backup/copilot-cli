# CI & Release Workflows Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace upstream AWS CI/CD with two lean GitHub Actions workflows: CI (test/lint on PR + push) and Release (build + publish binaries on tag push).

**Architecture:** Two workflow files. `ci.yml` runs 4 parallel jobs (test, mocks, build matrix, license) on PRs, pushes to mainline, and tags. `release.yml` triggers on `v*` tags only, gates on tests, then builds all platform binaries and creates a GitHub Release with assets.

**Tech Stack:** GitHub Actions, Go 1.23, Node.js 24.x, `softprops/action-gh-release@v2`

**Spec:** `docs/superpowers/specs/2026-04-16-ci-and-release-workflows-design.md`

---

### Task 1: Remove old CI/CD files

**Files:**
- Delete: `.github/workflows/ci.yml`
- Delete: `.github/workflows/commit.yml`
- Delete: `.github/workflows/homebrew.yml`
- Delete: `.github/workflows/stale.yml`
- Delete: `.github/workflows/doc_builder.yml`
- Delete: `.github/workflows/ci_size_computer.yml`
- Delete: `.github/workflows/ci_size_writer.yml`
- Delete: `.release/buildspec.yml`
- Delete: `.release/buildspec_e2e.yml`
- Delete: `.release/buildspec_integ.yml`
- Delete: `.release/buildspec_regression.yml`
- Delete: `.release/buildspec_sign.yml`
- Delete: `.release/buildspec_stage.yml`
- Delete: `.release/amazon-ecs-public-key.gpg`

- [ ] **Step 1: Delete all old workflow files**

```bash
rm .github/workflows/ci.yml \
   .github/workflows/commit.yml \
   .github/workflows/homebrew.yml \
   .github/workflows/stale.yml \
   .github/workflows/doc_builder.yml \
   .github/workflows/ci_size_computer.yml \
   .github/workflows/ci_size_writer.yml
```

- [ ] **Step 2: Delete the entire `.release/` directory**

```bash
rm -rf .release/
```

- [ ] **Step 3: Verify cleanup**

```bash
ls .github/workflows/
# Expected: empty directory
ls .release/ 2>&1
# Expected: "No such file or directory"
```

- [ ] **Step 4: Commit**

```bash
git add -u .github/workflows/ .release/
git commit -m "ci: remove upstream AWS CI/CD workflows and CodeBuild buildspecs"
```

---

### Task 2: Create new CI workflow

**Files:**
- Create: `.github/workflows/ci.yml`

- [ ] **Step 1: Create `.github/workflows/ci.yml`**

Write the following file:

```yaml
name: ci

on:
  pull_request:
    types: [opened, synchronize, reopened]
  push:
    branches: [mainline]
    tags: ['v*']

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: '1.23'

      - name: Set up Node
        uses: actions/setup-node@v4
        with:
          node-version: 24.x

      - name: Check out code
        uses: actions/checkout@v4

      - name: Run tests
        run: make local-test

  mocks:
    runs-on: ubuntu-latest
    steps:
      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: '1.23'

      - name: Set up Node
        uses: actions/setup-node@v4
        with:
          node-version: 24.x

      - name: Check out code
        uses: actions/checkout@v4

      - name: Generate mocks
        run: make gen-mocks

      - name: Verify no diff
        run: |
          if [ -n "$(git status --porcelain)" ]; then
            echo "Generated mocks are out of date. Run 'make gen-mocks' and commit."
            git diff
            exit 1
          fi
          echo "Generated mocks all match."

  build:
    runs-on: ubuntu-latest
    strategy:
      matrix:
        target: [compile-linux, compile-windows, compile-darwin]
    steps:
      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: '1.23'

      - name: Set up Node
        uses: actions/setup-node@v4
        with:
          node-version: 24.x

      - name: Check out code
        uses: actions/checkout@v4

      - name: Package custom resources
        run: make package-custom-resources

      - name: Build
        run: make ${{ matrix.target }}

      - name: Cleanup
        run: make package-custom-resources-clean

  license:
    runs-on: ubuntu-latest
    steps:
      - name: Check out code
        uses: actions/checkout@v4

      - name: Run license check
        run: ./scripts/license.sh .
```

- [ ] **Step 2: Validate YAML syntax**

```bash
python3 -c "import yaml; yaml.safe_load(open('.github/workflows/ci.yml'))" && echo "YAML valid"
```

Expected: `YAML valid`

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/ci.yml
git commit -m "ci: add CI workflow for tests, mocks, build, and license checks"
```

---

### Task 3: Create release workflow

**Files:**
- Create: `.github/workflows/release.yml`

- [ ] **Step 1: Create `.github/workflows/release.yml`**

Write the following file:

```yaml
name: release

on:
  push:
    tags: ['v*']

permissions:
  contents: write

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: '1.23'

      - name: Set up Node
        uses: actions/setup-node@v4
        with:
          node-version: 24.x

      - name: Check out code
        uses: actions/checkout@v4
        with:
          fetch-depth: 0

      - name: Run tests
        run: make local-test

  release:
    needs: test
    runs-on: ubuntu-latest
    steps:
      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: '1.23'

      - name: Set up Node
        uses: actions/setup-node@v4
        with:
          node-version: 24.x

      - name: Check out code
        uses: actions/checkout@v4
        with:
          fetch-depth: 0

      - name: Build all binaries
        run: make release

      - name: Create GitHub Release
        uses: softprops/action-gh-release@v2
        with:
          generate_release_notes: true
          files: |
            bin/local/copilot-darwin-amd64
            bin/local/copilot-darwin-arm64
            bin/local/copilot-linux-amd64
            bin/local/copilot-linux-arm64
            bin/local/copilot.exe
```

- [ ] **Step 2: Validate YAML syntax**

```bash
python3 -c "import yaml; yaml.safe_load(open('.github/workflows/release.yml'))" && echo "YAML valid"
```

Expected: `YAML valid`

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/release.yml
git commit -m "ci: add release workflow to build binaries and publish GitHub Releases on tag push"
```

---

### Task 4: Verify complete state

- [ ] **Step 1: Confirm only the two new workflows exist**

```bash
ls .github/workflows/
```

Expected output:
```
ci.yml
release.yml
```

- [ ] **Step 2: Confirm `.release/` is gone**

```bash
ls .release/ 2>&1
```

Expected: `No such file or directory`

- [ ] **Step 3: Verify Makefile targets referenced by workflows still work locally**

```bash
make package-custom-resources && make compile-local && make package-custom-resources-clean
./bin/local/copilot --version
```

Expected: binary builds and outputs `copilot version: <hash>` (hash because no tag exists yet).
