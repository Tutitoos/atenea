# GitHub workflow

This workflow was re-authored for ATENEA after reviewing Mailflow's issue-to-PR skills. Mailflow-specific roadmap, commands, repository locks, and product constraints do not apply.

## Issue mode

Inspect open and closed issues, milestones, labels, branches, PRs, and `origin/main`. Do not duplicate implemented or actively owned work. Create one reviewable issue with objective, scope, measurable acceptance, validation, dependencies, risks, evidence level, and exclusions. Re-read the created issue. Sensitive vulnerabilities use a private advisory; Dependabot PRs need no synthetic issue.

## Branch mode

Fetch without changing the worktree. Preserve all uncommitted work. Confirm the primary issue is open and no branch, worktree, or PR owns it. Start an isolated worktree at the selected `origin/main` SHA using `<category>/<issue>-<slug>` with `feat`, `fix`, `docs`, `refactor`, or `chore`. Verify branch, base, cleanliness, and single-writer ownership. An exact branch name mandated before this policy may remain unchanged and must be explained in its PR.

## Commit mode

Confirm the non-`main` branch, worktree, primary issue, and full status. Stage explicit task paths unless the complete dirty tree is verified as one authorized scope. Inspect the staged diff and scan for credentials, personal data, local databases, temporary transcripts, and unintended generated files. Run scope-appropriate repository checks. Use a Conventional Commit subject no longer than 72 characters. Do not amend or bypass hooks. Verify the resulting SHA and remaining status.

## PR mode

Verify GitHub authentication, `origin`, issue, branch, clean scope, commits ahead of current `origin/main`, and absence of a PR for the head. Push normally without rewriting shared history. Fill the repository PR template with exactly one `Closes #N`, observable behavior, exact validation, evidence limits, risks, and rollback. For a private security advisory, omit the public issue reference and use GitHub's private advisory link without exposing it in public text. Use a draft while required work remains. Re-read base, head, title, body, state, URL, issue link, and SHA.

## Review and merge mode

Record the exact head SHA and fetch checks, formal reviews, issue comments, inline comments, and unresolved GraphQL threads. Inspect the full diff and reproduce relevant checks. Classify findings as valid, fixed, outdated, inapplicable, or broader scope. Fix verified in-scope defects and restart review on every new SHA.

Wait at most 20 minutes per head SHA for expected checks, with a progress update at least once per minute. Retry an unchanged job once only when evidence shows an infrastructure or transient failure. Optional absent reviewers do not block unless repository rules require them.

Readiness requires successful required checks, satisfied acceptance criteria, no actionable thread, and an unchanged reviewed SHA. Never self-approve, dismiss a human objection, use admin bypasses, or force-push. Merge only with authorization. After merge, verify merge commit, issue closure, remote branch deletion, and resulting `main` checks. Installation, release, migration, and deployment remain separate effects.

## ATENEA checks

- Go: formatting, `go mod tidy -diff`, `go vet ./...`, `$(go env GOPATH)/bin/golangci-lint run`, and `go test -race -count=1 ./...`.
- Dashboard: `bun run --cwd dashboard check`, build, and committed embedded assets.
- Swift helper: strict-concurrency tests when changed.
- Scripts: shell syntax and relevant Python tests.
- Full gate: `bash scripts/v1-readiness.sh` from a clean checkout.
- Provider, MCP, and client-real gates are opt-in. Missing credentials or partial observations never become passes.
