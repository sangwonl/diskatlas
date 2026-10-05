# DiskAtlas

DiskAtlas is a local disk usage explorer. It maps folders by size and recent change so you can move from the largest areas into their contents, discover where storage goes, and choose what to inspect or remove.

## Run it

```sh
npm run scan -- ~
node bin/diskatlas.mjs report
node bin/diskatlas.mjs explain node.modules
node bin/diskatlas.mjs plan
node bin/diskatlas.mjs clean --dry-run
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
wails dev -appargs "--root /tmp/diskatlas-fixture"
# Positional shorthand:
wails dev -appargs "/tmp/diskatlas-fixture"
```

The Wails app displays operating-system disk capacity and available space immediately. An explicit analysis stores a local file manifest and a platform change cursor: macOS uses FSEvents, Windows uses the USN journal, and Linux uses an inotify journal while the app is running. When the cursor reports no changes, the previous result is returned without a filesystem walk. When a journal provides paths, only affected paths and cleanup candidates are refreshed; if a journal is unavailable, overflows, or cannot resolve affected paths, DiskAtlas rebuilds metadata conservatively instead of trusting directory mtimes. The configured scan root controls which files are inventoried; system data and unclassified usage remain explicitly separate. Shared filesystem blocks mean category totals are estimates, not guaranteed reclaimable space.

Directory discovery uses a bounded Go reader pool, and cleanup candidates are measured by a second bounded worker pool. Each category initially shows its three largest candidates, with an option to expand the rest. Individual and group actions open a review dialog where you can reveal the file's location before deletion. Hold the delete button to confirm; completed items are removed from the current scan without starting another full analysis.

The result also summarizes large user directories from the complete manifest. A folder such as `Documents/Obsidian` can therefore be surfaced even when its files are individually small; the folder summary is for review and opens a filtered view of the actual file candidates inside it.

Every manifest entry receives classification labels while it is read. Those labels help explain whether a path is generated dependencies, a Git repository, known app data, or managed storage before cleanup actions are offered. Providers can replace direct path deletion with a fixed native command: for example, when Docker reports reclaimable resources, DiskAtlas shows one Docker cleanup candidate and keeps Docker's raw storage protected from direct deletion. The command is only run after the same hold confirmation and is executed without a shell; DiskAtlas does not run it during scanning.

## Safety model

- Scanning never follows symbolic links. Known app data is classified by app and version; the newest version is kept out of the cleanup list, while older caches and logs are separated from settings and plugin data.
- System, credential, and application paths are `protected` and can never be selected. Git-tracked data is `review` and requires explicit acknowledgement.
- In addition to developer caches, the manifest records regular files larger than 10 MB. The GUI defaults to showing files larger than 20 MB and lets the user change the minimum size and age filters. These are `review` candidates because size and age alone do not prove that a personal file is disposable.
- A project-local `.shedignore` can hide generated paths from discovery.
- Existing scan history and folder permissions stay under their current `.shed` / `Shed` locations so this rename does not discard them. `.shedignore` remains supported.
- `clean` is a dry run unless `--quarantine --yes` is supplied.
- Quarantine only moves safe-tier artifacts discovered under the current user's home directory. It records a manifest and can be reversed with `restore --last`.
- The native app accepts cleanup IDs only from the latest in-memory scan. It rechecks the path, rule, protection status, symlinks, Git state, and open file state immediately before cleanup.
- Permanent deletion rechecks the path, rule, protection status, symlinks, Git state, and open file state, then uses a press-and-hold confirmation in the native UI. Caution and review items still receive the same backend safety checks.
- Quarantine requires `QUARANTINE N`; moving data on the same disk is recoverable but does not normally free disk space.

The CLI remains dependency-free Node.js 22 code for now. The native app is Go 1.25 + Wails v2 and runs without starting the browser server.

## Commands

```text
diskatlas scan [paths...] [--json]
diskatlas report [--tier safe|caution|review|protected] [--ecosystem node|python] [--min-size 1GB] [--older 90d] [--include path] [--exclude path] [--json]
diskatlas explain <path|rule-id> [--json]
diskatlas plan [filters] [--json]
diskatlas clean [filters] [--dry-run] [--quarantine --yes]
diskatlas restore <batch|--last>
diskatlas quarantine list
diskatlas quarantine purge [--yes]      # removes only DiskAtlas quarantine data
diskatlas history
diskatlas doctor
diskatlas gui [--port 4173] [--no-open]
diskatlas rules list|show <rule-id> [--json]
```

`--quarantine --yes` performs a second filesystem check and skips symlinks, protected paths, and directories currently reported by `lsof` as in use. The native GUI uses the same checks for quarantine and permanent deletion.
