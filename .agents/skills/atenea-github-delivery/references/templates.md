# Delivery templates

## Issue

```markdown
## Objective
## Scope
## Acceptance criteria
- [ ] Observable result
## Validation and evidence level
## Dependencies and risks
## Out of scope
```

## Branch

`<feat|fix|docs|refactor|chore>/<issue-number>-<concise-kebab-slug>`

## Commit

`<type>(<optional-scope>): <observable outcome>`

## Pull request

```markdown
## Related issue
Closes #N
## Result
## Plan and evidence
## Validation
## Risks and rollback
```

Use one primary issue and one closing reference. For a private security advisory,
omit both public references and use GitHub's private advisory link. Record unrun
validation with its reason.
