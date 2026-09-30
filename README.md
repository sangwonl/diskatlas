# Shed

Shed is a local disk cleanup app for developer caches and build outputs. It explains why a path can be removed, groups related candidates, and lets the user reclaim the space from the native app.

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

The Wails app automatically analyzes the current user's files on launch. It displays operating-system disk capacity and available space, plus a segmented chart of readable user files by type. System data and unclassified usage remain explicitly separate; external disks and other users' files are not inventoried. Shared filesystem blocks mean category totals are estimates, not guaranteed reclaimable space.

Cleanup candidates appear as they are measured by a bounded Go worker pool. Each category initially shows its three largest candidates, with an option to expand the rest. Individual and group actions open a review dialog where you can reveal the file's location before permanent deletion. Personal-file acknowledgement and deletion confirmation happen there.

## Safety model

- Scanning never follows symbolic links and ignores known application and protected paths.
- System, credential, and application paths are `protected` and can never be selected. Git-tracked data is `review` and requires explicit acknowledgement.
- In addition to developer caches, a scan shows regular files larger than 500 MB and files larger than 100 MB that have not changed for 180 days. These are `review` candidates because size and age alone do not prove that a personal file is disposable.
- A project-local `.shedignore` can hide generated paths from discovery.
- `clean` is a dry run unless `--quarantine --yes` is supplied.
- Quarantine only moves safe-tier artifacts discovered under the current user's home directory. It records a manifest and can be reversed with `restore --last`.
- The native app accepts cleanup IDs only from the latest in-memory scan. It rechecks the path, rule, protection status, symlinks, Git state, and open file state immediately before cleanup.
- Permanent deletion requires typing an exact `DELETE N` confirmation. Caution and review items also require explicit risk acknowledgement.
- Quarantine requires `QUARANTINE N`; moving data on the same disk is recoverable but does not normally free disk space.

The CLI remains dependency-free Node.js 22 code for now. The native app is Go 1.25 + Wails v2 and runs without starting the browser server.

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

`--quarantine --yes` performs a second filesystem check and skips symlinks, protected paths, and directories currently reported by `lsof` as in use. The native GUI uses the same checks for quarantine and permanent deletion.
