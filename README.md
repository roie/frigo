# frigo

`frigo` gives selected files and directories their own local Git history without moving them out of the working tree.

## Workflow

```bash
frigo add PLAN.md docs/local/
frigo status
frigo diff
frigo commit -m "update plan" PLAN.md
frigo commit -am "checkpoint"
frigo release PLAN.md
```

- `add` registers exact paths.
- `commit -m` saves chosen paths.
- `commit -am` saves all owned changes.
- `release PLAN.md` stops tracking an exact path after you are done with its frigo history.
- Use `release --force PLAN.md` only when uncommitted frigo changes intentionally remain.
- `status`, `diff`, `list`/`ls`, `log`, and `restore` inspect or recover the private history.

## Build from source

Requirements:

- Git 2.23 or newer on `PATH`
- A Git worktree
- Go 1.22

Build the binary locally:

```bash
go build -trimpath -o frigo ./cmd/frigo
```

This repository has no published module URL, so source builds are the supported install path for now.

## Behavior

- `frigo` does not keep a persistent staging area. It uses temporary indexes during operations and removes them afterward.
- A committed `.gitignore` rule outranks `.git/info/exclude`. A higher-priority ignore rule can re-include a path and make `frigo add` reject it.
- Each linked worktree gets its own local frigo metadata and history. The main repository’s `.git/info/exclude` section is rebuilt from the union of active worktree manifests.
- The history lives inside Git metadata. Deleting `.git` or a linked-worktree’s Git metadata deletes that history.
- `git clean -fdx` can still remove ignored working files. Saved frigo versions can be restored; unsaved edits cannot.
- History is local and unencrypted. Do not store secrets here.

## Safety limits

`frigo` is convenience tooling, not a security boundary. `git add -f`, direct index edits, or ignore-file changes can bypass it.

## v1 limits

No remotes, sync, external storage, hooks, daemon, TUI, automatic saves, or AI-specific adapters.
