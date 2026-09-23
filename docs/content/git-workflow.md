---
title: Git and GitHub delivery
weight: 42
---

# Git and GitHub delivery

ATENEA tracks work in [ATENEA Workflow](https://github.com/users/Tutitoos/projects/4). A Project draft task owns the acceptance checklist and may have one level of subtasks. Each reviewable implementation still uses one primary GitHub issue:

```text
Project task -> issue -> branch/worktree -> implementation -> validation
             -> commit -> push -> pull request -> checks/review
             -> authorized merge -> installation
```

New branches use `<category>/<issue-number>-<slug>` with `feat`, `fix`, `docs`, `refactor`, or `chore`, start from a verified `origin/main`, and have one writer in an isolated worktree. A branch name explicitly fixed by an accepted earlier plan may remain unchanged when its PR records the exception and still links its primary issue.

Every non-bot PR targets `main`, includes exactly one `Closes #N`, states exact validation and evidence levels, and identifies unrun checks. A private security advisory is the sole exception: keep its reference private, use GitHub's private advisory link, and do not create or mention a public issue. A check or review applies only to the head SHA it inspected. Every push restarts readiness review.

Review checks, formal reviews, issue comments, inline comments, and unresolved threads. Verify automated findings in code. Readiness requires successful required checks, satisfied acceptance criteria, no actionable thread, and an unchanged reviewed SHA. Never self-approve or bypass protection. Merge, installation, release, migration, and deployment are distinct effects.

Use the installed `$floowgithub` skill for the operational workflow and deterministic snapshot checker. ATENEA's machine-readable policy lives in `.github/floowgithub.json`; project-specific validation commands remain in `AGENTS.md`.

Project tasks use `Backlog`, `In Progress`, `Needs Fix`, `QA`, and `Completed`. Record the logical task ID in the `Parent Task` field of linked issues and PRs. Update checklist items only after checking current evidence. `Completed` requires finished subtasks, closed issues, merged PRs, successful checks, and final QA evidence; creating a PR does not complete the task.
