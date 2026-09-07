---
title: Git and GitHub delivery
weight: 42
---

# Git and GitHub delivery

ATENEA uses one primary GitHub issue for each reviewable implementation:

```text
issue -> branch/worktree -> implementation -> validation -> commit -> push
      -> pull request -> checks/review -> authorized merge -> installation
```

New branches use `<category>/<issue-number>-<slug>` with `feat`, `fix`, `docs`, `refactor`, or `chore`, start from a verified `origin/main`, and have one writer in an isolated worktree. A branch name explicitly fixed by an accepted earlier plan may remain unchanged when its PR records the exception and still links its primary issue.

Every non-bot PR targets `main`, includes exactly one `Closes #N`, states exact validation and evidence levels, and identifies unrun checks. A private security advisory is the sole exception: keep its reference private, use GitHub's private advisory link, and do not create or mention a public issue. A check or review applies only to the head SHA it inspected. Every push restarts readiness review.

Review checks, formal reviews, issue comments, inline comments, and unresolved threads. Verify automated findings in code. Readiness requires successful required checks, satisfied acceptance criteria, no actionable thread, and an unchanged reviewed SHA. Never self-approve or bypass protection. Merge, installation, release, migration, and deployment are distinct effects.

Use `.agents/skills/atenea-github-delivery/` for the operational workflow and deterministic snapshot checker.
