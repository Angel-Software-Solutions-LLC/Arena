# Repository guidance for Angel-Software-Solutions-LLC/Arena

`AGENTS.md` is the canonical instruction entrypoint for this repository.

## Start here

1. Read this file before changing repository state.
2. Read `README.md` and the repository-specific rules listed below.
3. Inspect the current branch, working tree, and open pull requests before
   editing.
4. Preserve user changes and repository-specific conventions.

## Repository-specific rules

Read [`.github/agent-policy/project-rules.md`](.github/agent-policy/project-rules.md) before non-trivial work. Those rules add project context; this file controls delivery and precedence.

## Delivery rules

- Never push directly to `develop` or `main`. Never force-push or weaken branch
  protection to get work merged.
- Use a task-specific branch. Keep one coherent work item on one branch and in
  one pull request.
- Before opening a pull request, search for an existing pull request for the
  same issue or task. Update that branch instead of creating a competing PR.
- Open normal change pull requests against `develop`. Only release pull
  requests from `develop` may target `main`.
- Rebase or merge the latest target branch before final review. Resolve
  conflicts deliberately and rerun validation after the resolution.
- Do not merge while required CI checks are failing.
- Stage only intended files. Do not discard, overwrite, or reformat unrelated
  work. Do not use destructive Git commands unless the operator explicitly
  requests them.
- Treat local hooks as early feedback. GitHub branch protection and required CI
  are the authoritative merge gate; never bypass either.

Human reviews, code-owner approval, issue linkage, PR orchestrators, and
automatic merging are optional. They are not merge requirements.

## Pull request discipline

- Explain what changed, why, risks, and exact validation evidence.
- A pull request adding behavior includes tests or explains why a mechanical
  test is not possible.
- For a bug fix, record the failing behavior or regression proof before the fix
  when practical.
- If another open pull request touches the same files, coordinate or consolidate
  when practical.
- Keep generated files, dependency updates, formatting, and behavior changes in
  separate pull requests unless they are inseparable.

## Validation

Run the smallest relevant checks while iterating and the full repository gate
before declaring the work ready. The current minimum commands are:

```bash
cd go-arena && go test ./...
```

```bash
git diff --check
```

If a documented command is unavailable on the current host, report that
limitation and rely on the required CI job; do not claim the command passed.

## Keeping this contract healthy

- Update `AGENTS.md` when the repository's true workflow changes.
- Keep `CLAUDE.md` as a compatibility pointer to this file.
- Put detailed, repository-specific material in
  `.github/agent-policy/project-rules.md` or an existing linked runbook.
