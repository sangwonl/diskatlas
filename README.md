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

The GUI is served at `http://127.0.0.1:4173`. It is intentionally a local browser UI so it can run without a framework, native modules, or network access.

## Safety model

- Scanning never follows symbolic links and ignores known application and protected paths.
- Git tracked paths are promoted to `protected` and cannot be selected.
- `clean` is a dry run unless `--quarantine --yes` is supplied.
- Quarantine only moves safe-tier artifacts discovered under the current user's home directory. It records a manifest and can be reversed with `restore --last`.
- Permanent deletion and `quarantine purge` are disabled in this build.

The current implementation is dependency-free Node.js 22 code. The core boundaries (`scan`, `report`, `explain`, `plan`, `clean`, restore, and GUI API) are kept separate so a future Wails shell can bind to the same core without adding cleanup logic to the UI.

## Commands

```text
shed scan [paths...] [--json]
shed report [--tier safe|caution|review|protected] [--ecosystem node|python] [--min-size 1GB] [--older 90d] [--include path] [--exclude path] [--json]
shed explain <path|rule-id> [--json]
shed plan [filters] [--json]
shed clean [filters] [--dry-run] [--quarantine --yes]
shed restore <batch|--last>
shed quarantine list
shed quarantine purge              # intentionally blocked
shed history
shed doctor
shed gui [--port 4173] [--no-open]
shed rules list|show <rule-id> [--json]
```
