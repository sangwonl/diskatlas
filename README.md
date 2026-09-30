# Shed

Shed is a local, safety-first disk report for developer caches and build outputs. It explains why a path is reclaimable before it appears in a cleanup plan.

## Run it

```sh
npm run scan -- ~
node bin/shed.mjs report
node bin/shed.mjs explain node.modules
node bin/shed.mjs plan
node bin/shed.mjs clean --dry-run
npm run gui
```

The browser GUI is served at `http://127.0.0.1:4173`. The native Wails GUI is launched from the repository root with:

```sh
loadgvm
gvm use go1.25.0
wails dev
```

The Wails shell calls the read-only Go scanner through generated bindings. It does not expose deletion or quarantine methods to the native UI.

## Safety model

- Scanning never follows symbolic links and ignores known application and protected paths.
- System, credential, and application paths are `protected`; Git-tracked user data is `review` and requires explicit acknowledgement before a recoverable move.
- A project-local `.shedignore` can hide generated paths from discovery.
- `clean` is a dry run unless `--quarantine --yes` is supplied.
- Quarantine only moves safe-tier artifacts discovered under the current user's home directory. It records a manifest and can be reversed with `restore --last`.
- Permanent deletion of scan targets is disabled. `quarantine purge --yes` can remove only items already inside Shed's own quarantine directory.

The CLI remains dependency-free Node.js 22 code for now. The native shell is Go 1.25 + Wails v2 and has its own read-only Go scanner under `internal/core`; this lets the Wails app run without starting the browser server.

## Commands

```text
shed scan [paths...] [--json]
shed report [--tier safe|caution|review|protected] [--ecosystem node|python] [--min-size 1GB] [--older 90d] [--include path] [--exclude path] [--json]
shed explain <path|rule-id> [--json]
shed plan [filters] [--json]
shed clean [filters] [--dry-run] [--quarantine --yes]
shed restore <batch|--last>
shed quarantine list
shed quarantine purge [--yes]      # removes only Shed quarantine data
shed history
shed doctor
shed gui [--port 4173] [--no-open]
shed rules list|show <rule-id> [--json]
```

`--quarantine --yes` performs a second filesystem check and skips symlinks, protected paths, and directories currently reported by `lsof` as in use. The GUI's cleanup action is preview-only by design.
