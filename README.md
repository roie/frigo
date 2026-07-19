# frigo

**Keep local project files without adding them to your main Git history.**

Some files belong inside a project but not in its main Git history:

- `PLAN.md`
- working notes
- research
- local documentation
- AI context files
- machine-specific configuration

frigo keeps those files in their normal project paths and gives them a separate, local Git-backed history.

```bash
frigo add PLAN.md
frigo commit -m "Add implementation plan" PLAN.md
```

Your file stays where it is. Main Git leaves it alone. frigo tracks its changes.

## Why frigo?

`frigo` is French for "fridge".

Think of it as a local compartment inside your project: the files stay where you use them, while their history stays separate from the main repository.

The `go` ending is also a small nod to Go, the language frigo is built with.

## Install

Install from npm:

```bash
npm install -g frigo
```

Or run without a global install:

```bash
npx frigo --help
```

Requirements:

- An existing Git repository
- Git 2.23 or newer on `PATH`

## Quick start

Add a file to frigo:

```bash
frigo add PLAN.md
```

This assigns `PLAN.md` to frigo. The file remains in the project, but ordinary operations in the main Git repository will leave it out.

Review its current state:

```bash
frigo status
frigo diff
```

Commit it to frigo history:

```bash
frigo commit -m "Add implementation plan" PLAN.md
```

Continue editing the file normally:

```bash
$EDITOR PLAN.md
```

Review and commit another revision:

```bash
frigo diff PLAN.md
frigo commit -m "Update implementation plan" PLAN.md
```

No separate initialization or staging step is required.

## Example

Given this project:

```text
my-project/
├── .git/
├── src/
├── README.md
├── PLAN.md
└── docs/
    └── research.md
```

Assign local planning files to frigo:

```bash
frigo add PLAN.md docs/research.md
```

The project now has two separate histories over the same directory:

```text
Main Git history
├── src/
└── README.md

frigo history
├── PLAN.md
└── docs/research.md
```

There is still only one physical copy of each file.

```bash
git status
```

shows changes for the main repository.

```bash
frigo status
```

shows changes for frigo-managed files.

## Commands

```text
frigo
frigo add [--] <path>...
frigo release [--force] [--] <path>...
frigo status
frigo list
frigo ls
frigo diff [--] [<path>...]
frigo commit -m <message> [--] <path>...
frigo commit -a -m <message>
frigo commit -am <message>
frigo log
frigo restore [--] <path>...
frigo help
frigo --help
frigo --version
```

Running `frigo` without arguments prints concise usage. Use `frigo help` or `frigo --help` for detailed help.

## Add files

```bash
frigo add PLAN.md
frigo add PLAN.md NOTES.md
frigo add docs/local/
```

`add` assigns existing files or directories to frigo.

It:

1. keeps the paths in their current location;
2. registers them as frigo-managed paths;
3. locally excludes them from the main Git repository;
4. includes them in frigo's separate history.

It does not modify the repository's shared `.gitignore`.

It also does not create a commit. Use `frigo commit` when you are ready to record the files.

When you add a directory, frigo manages it as a container. Files created inside that directory later automatically belong to frigo too.

## Check status

```bash
frigo status
```

frigo reports changes to its managed files, including:

- files that have never been committed;
- modified files;
- deleted files;
- changes inside managed directories;
- problems with the main Git ignore boundary.

## List managed paths

```bash
frigo list
```

The shorter alias is:

```bash
frigo ls
```

This lists the exact files and directories currently assigned to frigo.

## Review changes

Show all uncommitted frigo changes:

```bash
frigo diff
```

Limit the diff to selected paths:

```bash
frigo diff PLAN.md
frigo diff docs/local/
```

The comparison is against the latest frigo commit.

frigo does not expose a persistent staging area, so there is no staged diff.

## Commit changes

Commit selected managed paths:

```bash
frigo commit -m "Update implementation plan" PLAN.md
```

Commit several selected paths:

```bash
frigo commit -m "Update local documentation" PLAN.md docs/local/
```

Commit every changed path currently managed by frigo:

```bash
frigo commit -a -m "Checkpoint local project files"
```

frigo commits only the paths you select, or all managed changes when `-a` is used. Unrelated files from the main repository are not included.

A commit requires either one or more paths or `-a`. Running `frigo commit -m "message"` without either is rejected.

## View history

```bash
frigo log
```

This shows commits from frigo's separate history, not commits from the main repository.

## Restore files

Discard uncommitted changes to a managed file:

```bash
frigo restore PLAN.md
```

Restore several paths:

```bash
frigo restore PLAN.md docs/local/
```

The files are restored from the latest frigo commit.

Be careful: current uncommitted changes to those paths will be lost.

## Release files

Stop managing a path with frigo:

```bash
frigo release PLAN.md
```

Releasing a path:

- keeps the physical file in place;
- removes it from frigo's active managed paths;
- makes it visible to the main Git repository again;
- preserves its existing frigo history.

frigo refuses to release a path with uncommitted frigo changes.

Re-adding a previously released path resumes its existing frigo history; earlier commits are not removed.

To release a path despite uncommitted frigo changes:

```bash
frigo release --force PLAN.md
```

`release` does not delete the file.

To delete a file, delete it normally and commit that deletion to frigo:

```bash
rm PLAN.md
frigo commit -m "Remove implementation plan" PLAN.md
```

## How it works

frigo uses Git internally, but it is not a replacement Git interface.

It manages two concepts:

1. which project paths belong to frigo rather than the main repository;
2. a separate Git history for those paths.

Under the hood, frigo maintains another Git repository connected to the same project directory.

```text
                    project directory
                           │
             ┌─────────────┴─────────────┐
             │                           │
        main Git                     frigo
     main history                 local history
```

frigo also manages repository-local ignore rules so ordinary commands such as:

```bash
git add -A
git commit
```

leave frigo-managed files out of the main repository.

Specifically, frigo writes managed paths to `.git/info/exclude`, or to the equivalent `$GIT_COMMON_DIR/info/exclude` when Git uses shared metadata, such as with linked worktrees. Git provides this for repository-specific files that should remain local instead of being shared through `.gitignore`.

No Git hooks are installed. No daemon runs in the background. No shared configuration file is added to the project.

## Why not `.gitignore`?

A shared `.gitignore` is appropriate when a file should be ignored by everyone using the repository.

frigo is for files that belong only to the current clone or developer workflow.

For example, your personal `PLAN.md` may belong inside the project without becoming a repository-wide convention.

frigo manages that locally, without creating a `.gitignore` change for the rest of the project.

Unlike an ordinary ignored file, a frigo-managed file can still have:

- status;
- diffs;
- commits;
- history;
- restoration.

## frigo and coding agents

frigo-managed files remain ordinary files in the project directory.

Coding agents can read and edit them directly when given their paths:

```text
Read PLAN.md and continue from the current plan.
```

frigo also helps prevent normal agent-driven Git operations from committing those files to the main repository.

Automatic file discovery varies by editor and agent. Some tools omit Git-ignored files from search results, repository maps, or file pickers even though the files remain accessible by path.

frigo does not install agent-specific instructions or modify files such as `AGENTS.md`, `CLAUDE.md`, or editor settings.

## Limitations

frigo is convenience tooling, not a security boundary. Deliberate force-adds, direct index changes, or modified ignore rules can bypass it.

### frigo history is local

frigo history belongs to the current repository clone.

It is not pushed with the main repository and is not automatically available from another clone or computer.

### Git metadata contains the history

frigo stores its history alongside the current clone's Git metadata.

Removing or rebuilding `.git` also removes the frigo history for that clone.

frigo is not a backup service.

### Main Git protection is practical, not absolute

frigo protects files from ordinary main-repository staging and commit workflows.

Repository ignore rules can conflict with frigo's local rules. frigo validates managed paths during its commands and reports detected conflicts, but later changes outside frigo can still alter the effective Git behavior.

Review the main repository's staged diff before committing sensitive files.

### frigo is not secret storage

frigo-managed files remain normal readable files on disk.

frigo does not encrypt them, restrict filesystem access, or make them safe for credentials and secrets.

Use an appropriate secret manager for passwords, tokens, private keys, and other sensitive values.

### Editor integration is separate

frigo-managed files remain visible and editable in the filesystem, but their frigo history does not automatically appear as a second source-control provider in editors such as VS Code.

Use the frigo CLI to inspect status, diffs, and history.

## Building from source

Source builds require Go 1.26 or newer:

```bash
go install github.com/roie/frigo/cmd/frigo@latest
```

For local development:

```bash
go build -trimpath -o frigo ./cmd/frigo
```

## Uninstall

```bash
npm uninstall -g frigo
```

This removes only the command. Existing frigo history and local ignore entries remain unchanged.

Do not delete frigo's project metadata directly. Managed paths may remain excluded from the main repository.

## License

Apache-2.0
