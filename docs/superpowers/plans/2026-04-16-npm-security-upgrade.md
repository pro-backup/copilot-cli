# npm Security Upgrade Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve all 22 npm Dependabot alerts (alerts #1, #4, #9, #11, #13, #14, #16, #23-40) by upgrading production and dev dependencies, replacing abandoned tooling, and updating CI configuration. The 7 Go alerts (#41-47) are out of scope.

**Architecture:** All npm code lives in `cf-custom-resources/`. Production deps are AWS SDK v3 clients; dev deps include jest, eslint, babel-minify, nock, sinon, and several pinned security overrides. The upgrade replaces `babel-minify` (abandoned) with `terser`, bumps `eslint` from v7 to v8 (with matching config plugins), upgrades `nock` to v13.5.x (drops lodash.set), bumps all AWS SDK clients to latest, and removes stale security-override devDeps. CI Node version moves from 16.x to 24.x to match the Lambda runtime.

**Tech Stack:** Node.js, Jest, ESLint, Terser, AWS SDK v3, GitHub Actions

---

### Task 1: Add Jest Coverage Thresholds (Safety Net)

**Files:**
- Modify: `cf-custom-resources/jest.config.js`

This must be done FIRST before any dependency changes, so we have a gate that catches regressions.

- [ ] **Step 1: Update jest.config.js to add coverage thresholds**

Add coverage collection and thresholds based on the current baseline (92% statements, 81% branches, 83% functions, 92% lines). Set thresholds slightly below current to catch regressions without being brittle:

```javascript
// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0
module.exports = {
  roots: ["<rootDir>/lib", "<rootDir>/test"],
  transform: {
    "^.+\\.tsx?$": "ts-jest",
  },
  testRegex: "(/test/.*|(\\.|/)(test|spec))\\.(ts|js)x?$",
  moduleFileExtensions: ["ts", "tsx", "js", "jsx", "json", "node"],
  testEnvironment: "node",
  collectCoverageFrom: [
    "lib/**/*.js",
    "!lib/**/node_modules/**",
  ],
  coverageThreshold: {
    global: {
      statements: 90,
      branches: 78,
      functions: 80,
      lines: 90,
    },
  },
};
```

- [ ] **Step 2: Verify coverage thresholds pass with current code**

Run:
```bash
cd cf-custom-resources && npx jest --coverage
```
Expected: All 14 test suites pass, coverage meets thresholds (92%+ statements, 81%+ branches).

- [ ] **Step 3: Commit**

```bash
git add cf-custom-resources/jest.config.js
git commit -m "chore: add Jest coverage thresholds as safety net for dependency upgrades"
```

---

### Task 2: Upgrade AWS SDK v3 Clients to Latest

**Files:**
- Modify: `cf-custom-resources/package.json`
- Regenerated: `cf-custom-resources/package-lock.json`

This fixes: `fast-xml-parser` (4 CVEs: critical+high+medium+low), `@smithy/config-resolver` (low, defense-in-depth).

- [ ] **Step 1: Upgrade all AWS SDK v3 production dependencies**

Run:
```bash
cd cf-custom-resources && npm install \
  @aws-sdk/client-acm@latest \
  @aws-sdk/client-apprunner@latest \
  @aws-sdk/client-cloudformation@latest \
  @aws-sdk/client-ecs@latest \
  @aws-sdk/client-elastic-load-balancing-v2@latest \
  @aws-sdk/client-resource-groups-tagging-api@latest \
  @aws-sdk/client-route-53@latest \
  @aws-sdk/client-s3@latest \
  @aws-sdk/client-sfn@latest \
  @aws-sdk/client-sqs@latest \
  @aws-sdk/core@latest \
  @aws-sdk/credential-providers@latest
```

- [ ] **Step 2: Upgrade aws-sdk-client-mock to latest compatible version**

The test mock library must be compatible with the new SDK versions:
```bash
cd cf-custom-resources && npm install --save-dev aws-sdk-client-mock@latest
```

- [ ] **Step 3: Run tests to verify no regressions**

Run:
```bash
cd cf-custom-resources && npx jest --coverage
```
Expected: All 14 test suites pass, coverage thresholds met. If any tests fail due to SDK API changes, fix the test mocks — the `aws-sdk-client-mock` library abstracts most SDK internals, so failures are unlikely.

- [ ] **Step 4: Verify no fast-xml-parser or @smithy/config-resolver audit findings remain**

Run:
```bash
cd cf-custom-resources && npm audit 2>&1 | grep -E "fast-xml-parser|@smithy/config-resolver"
```
Expected: No output (vulnerabilities resolved).

- [ ] **Step 5: Commit**

```bash
git add cf-custom-resources/package.json cf-custom-resources/package-lock.json
git commit -m "fix(security): upgrade AWS SDK v3 clients to latest

Resolves fast-xml-parser CVEs (entity expansion, DoS, regex injection)
and @smithy/config-resolver region validation advisory."
```

---

### Task 3: Replace babel-minify with terser

**Files:**
- Modify: `cf-custom-resources/package.json` (scripts.package, devDependencies)
- Regenerated: `cf-custom-resources/package-lock.json`

This fixes: `yargs-parser` (moderate, prototype pollution), `@babel/helpers` (moderate, regex). `babel-minify` is abandoned (last release 2020).

- [ ] **Step 1: Install terser and remove babel-minify**

```bash
cd cf-custom-resources && npm install --save-dev terser@latest && npm uninstall babel-minify
```

- [ ] **Step 2: Update the `package` script in package.json**

The old script was:
```json
"package": "minify lib -d ../internal/pkg/template/templates/custom-resources --builtIns false"
```

Replace with a shell script that minifies each file individually using terser (matching babel-minify's behavior of processing each file in `lib/` and outputting to the target directory):

```json
"package": "mkdir -p ../internal/pkg/template/templates/custom-resources && for f in lib/*.js; do terser \"$f\" --compress --mangle -o \"../internal/pkg/template/templates/custom-resources/$(basename $f)\"; done"
```

- [ ] **Step 3: Verify the package script produces valid minified output**

Run:
```bash
cd cf-custom-resources && npm run package
```
Expected: Minified `.js` files appear in `../internal/pkg/template/templates/custom-resources/`. Verify file count matches source:
```bash
ls -1 ../internal/pkg/template/templates/custom-resources/*.js | wc -l
```
Expected: `14` (one minified file per source module).

- [ ] **Step 4: Spot-check a minified file is valid JavaScript**

```bash
node -c ../internal/pkg/template/templates/custom-resources/dns-cert-validator.js
```
Expected: No syntax errors.

- [ ] **Step 5: Run the full build to verify Go embedding works**

```bash
cd /Users/bynov/go/src/github.com/pro-backup/copilot-cli && make build
```
Expected: Build succeeds, producing `./bin/local/copilot`.

- [ ] **Step 6: Clean up minified files**

```bash
make package-custom-resources-clean
```

- [ ] **Step 7: Run tests to verify nothing broke**

```bash
cd cf-custom-resources && npx jest --coverage
```
Expected: All 14 test suites pass (tests run against unminified source, so this should be unchanged).

- [ ] **Step 8: Commit**

```bash
git add cf-custom-resources/package.json cf-custom-resources/package-lock.json
git commit -m "fix(security): replace abandoned babel-minify with terser

babel-minify has not been maintained since 2020 and pulls in vulnerable
yargs-parser and @babel/helpers. terser is the actively maintained standard
JS minifier. The package script now minifies each lib/*.js file individually."
```

---

### Task 4: Upgrade ESLint from v7 to v8

**Files:**
- Modify: `cf-custom-resources/package.json` (devDependencies)
- Create: `cf-custom-resources/.eslintrc.json`
- Regenerated: `cf-custom-resources/package-lock.json`

This fixes: `semver` (high, ReDoS) in eslint's transitive deps. ESLint v8 still supports `.eslintrc` config (no flat config migration needed). The `eslint-config-standard@17` requires `eslint-plugin-n` (renamed from `eslint-plugin-node`).

- [ ] **Step 1: Remove old eslint plugins and install new versions**

```bash
cd cf-custom-resources && npm uninstall eslint eslint-config-standard eslint-plugin-import eslint-plugin-node eslint-plugin-promise eslint-plugin-standard
```

```bash
cd cf-custom-resources && npm install --save-dev \
  eslint@^8.57.1 \
  eslint-config-standard@^17.1.0 \
  eslint-plugin-import@^2.32.0 \
  eslint-plugin-n@^15.7.0 \
  eslint-plugin-promise@^6.6.0
```

Note: `eslint-plugin-standard` is no longer needed with `eslint-config-standard@17`. `eslint-plugin-node` is replaced by `eslint-plugin-n`.

- [ ] **Step 2: Create .eslintrc.json config file**

The old setup relied on `eslint-config-standard` being auto-discovered. Create an explicit config:

```json
{
  "extends": "standard",
  "env": {
    "node": true,
    "es2021": true,
    "jest": true
  },
  "parserOptions": {
    "ecmaVersion": 2021
  }
}
```

Write this to `cf-custom-resources/.eslintrc.json`.

- [ ] **Step 3: Run lint to verify config works**

```bash
cd cf-custom-resources && npx eslint lib/
```
Expected: No errors (or only pre-existing style warnings that were present before the upgrade).

- [ ] **Step 4: Run tests to verify nothing changed**

```bash
cd cf-custom-resources && npx jest --coverage
```
Expected: All 14 test suites pass.

- [ ] **Step 5: Commit**

```bash
git add cf-custom-resources/package.json cf-custom-resources/package-lock.json cf-custom-resources/.eslintrc.json
git commit -m "fix(security): upgrade eslint v7 to v8 with standard config v17

Resolves semver ReDoS vulnerability in eslint v7 transitive deps.
Replaces eslint-plugin-node with eslint-plugin-n (maintained fork).
Drops eslint-plugin-standard (no longer needed with standard@17)."
```

---

### Task 5: Upgrade nock and sinon

**Files:**
- Modify: `cf-custom-resources/package.json` (devDependencies)
- Regenerated: `cf-custom-resources/package-lock.json`

This fixes: `lodash.set` (high, prototype pollution), `lodash` (high+medium, prototype pollution + code injection). `nock@13.5.x` dropped the `lodash.set` dependency.

- [ ] **Step 1: Upgrade nock and sinon**

```bash
cd cf-custom-resources && npm install --save-dev nock@^13.5.6 sinon@^21.1.2
```

- [ ] **Step 2: Run tests to verify mocks still work**

```bash
cd cf-custom-resources && npx jest --coverage
```
Expected: All 14 test suites pass. `nock` API is backward-compatible within v13.x. `sinon` major version bumps may require test adjustments — if any tests fail, check the sinon changelog for breaking changes in the assertion API.

- [ ] **Step 3: Verify lodash.set is gone from dependency tree**

```bash
cd cf-custom-resources && npm ls lodash.set --all 2>&1
```
Expected: `(empty)` — lodash.set no longer in the tree.

- [ ] **Step 4: Commit**

```bash
git add cf-custom-resources/package.json cf-custom-resources/package-lock.json
git commit -m "fix(security): upgrade nock to 13.5.x and sinon to latest

Resolves lodash.set prototype pollution (nock dropped the dependency
in v13.5.x). Upgrades sinon to current maintained version."
```

---

### Task 6: Remove Stale Security Override devDependencies

**Files:**
- Modify: `cf-custom-resources/package.json` (devDependencies)
- Regenerated: `cf-custom-resources/package-lock.json`

Several devDependencies exist solely as version overrides to force-resolve transitive security issues. After the upgrades in Tasks 2-5, many of these are no longer needed because the parent packages have been upgraded.

- [ ] **Step 1: Remove override-only devDependencies**

These devDependencies are not directly imported by any source or test file — they were added only to force npm to resolve higher versions of transitive deps:

```bash
cd cf-custom-resources && npm uninstall \
  ansi-regex \
  axios \
  glob-parent \
  json-schema \
  minimist \
  set-value \
  tmpl \
  ws \
  yargs-parser
```

Note: Keep `minimatch` if it's used anywhere. Check first:
```bash
grep -r "require.*minimatch\|from.*minimatch" cf-custom-resources/lib/ cf-custom-resources/test/
```
If no results, also remove it:
```bash
cd cf-custom-resources && npm uninstall minimatch
```

- [ ] **Step 2: Run npm audit to check if any new transitives appeared**

```bash
cd cf-custom-resources && npm audit 2>&1
```

The `js-yaml@3.x` vulnerability (#13) will persist as a transitive dep of `@istanbuljs/load-nyc-config` (pulled in by ts-jest → babel-plugin-istanbul). The eslint path is fixed by Task 4 (eslint v8 uses js-yaml v4). Add an override for the remaining path, plus any other residual vulnerabilities:

```json
"overrides": {
  "js-yaml": ">=3.14.2"
}
```

If additional vulnerabilities remain due to transitive deps of the remaining packages (jest, lambda-tester, etc.), add them to the `overrides` block in the same way.

- [ ] **Step 3: Run tests to verify nothing broke**

```bash
cd cf-custom-resources && npx jest --coverage
```
Expected: All 14 test suites pass. These packages were never imported by source or test code.

- [ ] **Step 4: Commit**

```bash
git add cf-custom-resources/package.json cf-custom-resources/package-lock.json
git commit -m "chore: remove stale security-override devDependencies

These packages were added as devDependencies solely to force-resolve
transitive vulnerabilities. Parent packages have been upgraded and
no longer pull vulnerable versions."
```

---

### Task 7: Update CI Node.js Version

**Files:**
- Modify: `.github/workflows/ci.yml`

Node 16.x reached EOL in September 2023. The Lambda runtime was recently upgraded to Node 24 (commit f3a68e98). CI should match the Lambda runtime to catch compatibility issues early. All key packages (jest@29, eslint@8, terser, nock@13, lambda-tester) declare engine support for `>=18`, so Node 24 is compatible.

- [ ] **Step 1: Update all Node version references in ci.yml**

In `.github/workflows/ci.yml`, change every occurrence of:
```yaml
node-version: 16.x
```
to:
```yaml
node-version: 24.x
```

This appears in three jobs: `build` (line 25), `mocks` (line 50), `test` (line 77).

Also upgrade the `actions/setup-node` from v1 to v4:
```yaml
uses: actions/setup-node@v4
```

- [ ] **Step 2: Verify tests pass locally with Node 24**

Check your local Node version:
```bash
node --version
```
If it's not v24.x, use nvm or similar to test with Node 24:
```bash
nvm use 24 2>/dev/null || echo "Test with available Node version"
cd cf-custom-resources && npx jest --coverage
```
Expected: All 14 test suites pass.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/ci.yml
git commit -m "ci: upgrade Node.js from 16.x to 24.x to match Lambda runtime

Node 16 reached EOL in September 2023. Lambda runtime was upgraded
to Node 24 in f3a68e98. CI now matches the deployed runtime.
Also upgrades actions/setup-node from v1 to v4."
```

---

### Task 8: Upgrade E2E Test Package Dependencies

**Files:**
- Modify: `e2e/sidecars/hello/package.json`
- Modify: `e2e/app-with-domain/src/package.json`

These have Dependabot alerts for `koa` (high+medium: XSS, host injection) and `express` (transitive vulns). These are sample apps used only in E2E tests.

- [ ] **Step 1: Upgrade koa in e2e/sidecars/hello**

Edit `e2e/sidecars/hello/package.json` to update koa. Note: `koa-router` was renamed to `@koa/router` at v8+:
```json
{
  "dependencies": {
    "koa": "^2.16.4",
    "@koa/router": "^15.4.0",
    "lorem-ipsum": "1.0.4"
  },
  "scripts": {
    "start": "node server.js"
  }
}
```

- [ ] **Step 2: Migrate server.js from koa v1 to v2 syntax**

The current `e2e/sidecars/hello/server.js` uses koa v1 patterns (generators, `this.body`). Rewrite it for koa v2:

```javascript
// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

const Koa = require('koa');
const Router = require('@koa/router');
const lorem = require('lorem-ipsum');

const app = new Koa();
const router = new Router();

// Log requests
app.use(async (ctx, next) => {
  const start = new Date();
  await next();
  const ms = new Date() - start;
  console.log('%s %s - %s', ctx.method, ctx.url, ms);
});

router.get('/api/lorem-ipsum', (ctx) => {
  ctx.body = {
    body: lorem({
      count: 10,
      units: 'paragraphs'
    })
  };
});

router.get('/api/health-check', (ctx) => {
  ctx.body = 'Ready';
});

app.use(router.routes());
app.use(router.allowedMethods());

app.listen(3000);

console.log('Application ready and running...');
```

- [ ] **Step 3: Upgrade express in e2e/app-with-domain**

Edit `e2e/app-with-domain/src/package.json`:
```json
{
  "name": "project",
  "version": "1.0.0",
  "description": "",
  "main": "index.js",
  "scripts": {
    "test": "echo \"Error: no test specified\" && exit 1"
  },
  "keywords": [],
  "author": "",
  "license": "ISC",
  "dependencies": {
    "express": "^4.21.2"
  }
}
```

- [ ] **Step 4: Commit**

```bash
git add e2e/sidecars/hello/package.json e2e/sidecars/hello/server.js e2e/app-with-domain/src/package.json
git commit -m "fix(security): upgrade koa and express in e2e test apps

Resolves koa XSS and host header injection CVEs.
Upgrades express to latest v4 for transitive security fixes."
```

---

### Task 9: Final Validation

**Files:** None (verification only)

- [ ] **Step 1: Clean install and full audit**

```bash
cd cf-custom-resources && rm -rf node_modules package-lock.json && npm install && npm audit
```
Expected: 0 vulnerabilities (or only low-severity advisories that have no fix available).

- [ ] **Step 2: Run full test suite with coverage**

```bash
cd cf-custom-resources && npx jest --coverage
```
Expected: All 14 test suites pass, all coverage thresholds met.

- [ ] **Step 3: Run lint**

```bash
cd cf-custom-resources && npx eslint lib/
```
Expected: No errors.

- [ ] **Step 4: Run full build**

```bash
cd /Users/bynov/go/src/github.com/pro-backup/copilot-cli && make build
```
Expected: Build succeeds, producing `./bin/local/copilot`.

- [ ] **Step 5: Clean up build artifacts**

```bash
make package-custom-resources-clean
```

- [ ] **Step 6: Verify Dependabot alerts will resolve**

Compare the npm audit output against the original Dependabot findings. All npm alerts (#23-40) should be resolved. Go alerts (#41-47) are out of scope for this plan.

- [ ] **Step 7: Commit lockfile if regenerated in Step 1**

```bash
git add cf-custom-resources/package-lock.json
git commit -m "chore: regenerate package-lock.json after all dependency upgrades"
```
