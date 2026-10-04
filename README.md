# Shed

Shed is a local disk cleanup app for developer caches, known app data, and build outputs. It explains why a path can be removed, groups related candidates, and lets the user reclaim the space from the native app.

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

The native app analyzes `/` by default. During development, pass a smaller fixture or project root through Wails' app arguments:

```sh
wails dev -appargs "--root /tmp/shed-fixture"
# Positional shorthand:
wails dev -appargs "/tmp/shed-fixture"
```

The Wails app displays operating-system disk capacity and available space immediately. An explicit analysis stores a local file manifest and a platform change cursor: macOS uses FSEvents, Windows uses the USN journal, and Linux uses an inotify journal while the app is running. When the cursor reports no changes, the previous result is returned without a filesystem walk. When a journal provides paths, only affected paths and cleanup candidates are refreshed; if a journal is unavailable, overflows, or cannot resolve affected paths, Shed rebuilds metadata conservatively instead of trusting directory mtimes. The configured scan root controls which files are inventoried; system data and unclassified usage remain explicitly separate. Shared filesystem blocks mean category totals are estimates, not guaranteed reclaimable space.

Directory discovery uses a bounded Go reader pool, and cleanup candidates are measured by a second bounded worker pool. Each category initially shows its three largest candidates, with an option to expand the rest. Individual and group actions open a review dialog where you can reveal the file's location before deletion. Hold the delete button to confirm; completed items are removed from the current scan without starting another full analysis.

The result also summarizes large user directories from the complete manifest. A folder such as `Documents/Obsidian` can therefore be surfaced even when its files are individually small; the folder summary is for review and opens a filtered view of the actual file candidates inside it.

Every manifest entry receives classification labels while it is read. Those labels are carried into the candidate explanation so a path can be identified as generated dependencies, a Git repository, known app data, or managed storage before a cleanup action is offered. Providers can replace direct path deletion with a fixed native command: for example, when Docker reports reclaimable resources, Shed shows one Docker cleanup candidate and keeps Docker's raw storage protected from direct deletion. The command is only run after the same hold confirmation and is executed without a shell; Shed does not run it during scanning.

## Safety model

- Scanning never follows symbolic links. Known app data is classified by app and version; the newest version is kept out of the cleanup list, while older caches and logs are separated from settings and plugin data.
- System, credential, and application paths are `protected` and can never be selected. Git-tracked data is `review` and requires explicit acknowledgement.
- In addition to developer caches, the manifest records regular files larger than 10 MB. The GUI defaults to showing files larger than 20 MB and lets the user change the minimum size and age filters. These are `review` candidates because size and age alone do not prove that a personal file is disposable.
- A project-local `.shedignore` can hide generated paths from discovery.
- `clean` is a dry run unless `--quarantine --yes` is supplied.
- Quarantine only moves safe-tier artifacts discovered under the current user's home directory. It records a manifest and can be reversed with `restore --last`.
- The native app accepts cleanup IDs only from the latest in-memory scan. It rechecks the path, rule, protection status, symlinks, Git state, and open file state immediately before cleanup.
- Permanent deletion rechecks the path, rule, protection status, symlinks, Git state, and open file state, then uses a press-and-hold confirmation in the native UI. Caution and review items still receive the same backend safety checks.
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
