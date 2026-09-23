---
title: Git and GitHub delivery
weight: 42
---

# Git and GitHub delivery

Each GitHub issue is the task and owns its acceptance checklist, child issues, and linked pull requests:

```text
issue -> branch/worktree -> implementation -> validation
      -> commit -> push -> pull request -> QA
      -> authorized merge -> issue closure -> installation
```

New branches use `<category>/<issue-number>-<slug>` with `feat`, `fix`, `docs`, `refactor`, or `chore`, start from a verified `origin/main`, and have one writer in an isolated worktree. A branch name explicitly fixed by an accepted earlier plan may remain unchanged when its PR records the exception and still links its primary issue.

Every non-bot PR targets `main`, links exactly one primary issue with `Refs #N` while work remains or `Closes #N` only when its merge completes the issue, states exact validation and evidence levels, and identifies unrun checks. A private security advisory is the sole exception: keep its reference private, use GitHub's private advisory link, and do not create or mention a public issue. QA evidence applies only to the head SHA it inspected. Every push restarts QA readiness.

QA includes code review, automated checks, and functional validation. Inspect issue comments, inline comments, and unresolved threads; verify automated findings in code. Merge readiness requires successful required checks, satisfied relevant acceptance criteria, no actionable thread, and an unchanged reviewed SHA. Never self-approve or bypass protection. Merge, issue closure, installation, release, migration, and deployment are distinct effects.

Use the installed `$floowgithub` skill for the operational workflow and deterministic snapshot checker. ATENEA's machine-readable policy lives in `.github/floowgithub.json`; repository-specific validation commands remain in `AGENTS.md`.

Update an issue checklist item when its observable result has current evidence; reopen it if that evidence is invalidated. Close a parent issue only after its checklist, child issues, required PRs, and QA are complete. Creating or merging a PR alone does not complete the issue.
