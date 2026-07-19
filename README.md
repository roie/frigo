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

## Install

Requirements:

- Git 2.23 or newer on `PATH`
- A Git worktree

Install from npm:

```bash
npm install -g frigo
```

Or run without a global install:

```bash
npx frigo --help
```

Source builds are also supported with Go 1.26.5 or newer:

```bash
go install github.com/roie/frigo/cmd/frigo@latest
```

For local development:

```bash
go build -trimpath -o frigo ./cmd/frigo
```

## Behavior

- `frigo` does not keep a persistent staging area. It uses temporary indexes during operations and removes them afterward.
- A committed `.gitignore` rule outranks `.git/info/exclude`. A higher-priority ignore rule can re-include a path and make `frigo add` reject it.
- Each linked worktree gets its own local frigo metadata and history. The main repository’s `.git/info/exclude` section is rebuilt from the union of active worktree registries.
- The history lives inside Git metadata. Deleting `.git` or a linked-worktree’s Git metadata deletes that history.
- `git clean -fdx` can still remove ignored working files. Saved frigo versions can be restored; unsaved edits cannot.
- History is local and unencrypted. Do not store secrets here.

## Safety limits

`frigo` is convenience tooling, not a security boundary. `git add -f`, direct index edits, or ignore-file changes can bypass it.

## v1 limits

No remotes, synchronization, hooks, daemon, TUI, partial-hunk commits, or destructive history commands.
