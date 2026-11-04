# Build-box lane (`just bb`)

Run tests and gates on a LAN build box instead of xsm. `just bb <command…>`, from any vecnet worktree:

1. picks a box (see Box choice below);
2. rsyncs the worktree to `~/bb-src/vecnet4/<lane>/src/` on the box (lane = worktree basename, lowercased);
3. installs the pinned toolchain on first use (about a minute, once per box);
4. runs the command there, with output streamed and the box's cores in use;
5. pulls back `SHOTS_DIR` and any source files the command wrote;
6. exits with the command's status.

```sh
just bb go test -count=1 ./...
just bb CGO_ENABLED=1 CC=/usr/bin/gcc go test -race ./internal/hub/
just bb build2 node --test --test-concurrency=2 internal/ui/jstest/
just bb SHOTS_DIR=.scratch/vecnet/mine/shots just ui-shots
just bb just validate-protocol
just bb bash -c '[ ! -f connectors/launcher/package.json ] || (just ln-setup && just ln-check)'
```

Commands are argv, not a shell string: use `bash -c '…'` for `||`, `&&`, pipes or globs. `NAME=VALUE` words in front of the command set the remote environment, and so do the allowlisted names (below) when they are set in the caller's environment, so `CGO_ENABLED=0 just bb go test ./...` works too.

## What runs where

| On xsm | On the box |
|---|---|
| `build2 <cmd>` (2-core slice) | `scripts/bb/bin/build2`: runs `<cmd>` directly and rewrites `go … -p N` to `-p $BB_JOBS` |
| `go … -p 2` | also rewritten to `-p $BB_JOBS` (default 8); `GOFLAGS=-p=$BB_JOBS` covers commands without `-p` |
| Go 1.27.1 (CachyOS) | Go 1.27.1 (upstream tarball) |
| Node 22.23.3 | Node 22.23.3 (pinned tarball; the box's own Node is not used) |
| Playwright from the headless-browser-screenshots skill | Playwright 1.61.1 from `scripts/bb/playwright/package-lock.json`, chromium-headless-shell 1228 |
| python3 + jsonschema 4.26.0 | a uv-built venv with the same pins, so `validate-protocol` checks the examples |
| Inter 4.1, JetBrains Mono 2.304 (system packages) | the same font files (byte-identical upstream releases), linked into the lane HOME for fontconfig |

Everything runs under `nice -n 10` with `timeout $BB_TIMEOUT` (default 7200 s). `-race` needs cgo: pass `CGO_ENABLED=1 CC=/usr/bin/gcc`; every candidate box has gcc at `/usr/bin/gcc`.

The remote command gets a clean environment (`env -i`): lane-private `HOME`, `TMPDIR` and `TMUX_TMPDIR`, shared `GOCACHE`/`GOMODCACHE`, `GOTOOLCHAIN=local`, and `VECNET_BB=1`. Nothing reads or writes the box user's dotfiles.

## Box choice and fallback

`BB_HOST=<host>` pins a box. Otherwise the first reachable host in `BB_HOSTS` (default `x-alpha x-tall x-p35 x-19`) wins. When the preferred box fails its probe (an mDNS miss costs about 7 s), the fallback is cached for 10 minutes in `$XDG_RUNTIME_DIR/vecnet-bb/host`, so a burst of checks does not pay the probe each time.

- **x-alpha** (48 threads): preferred. On 2026-11-04 it was unreachable from xsm: `x-alpha.local` did not resolve, `x-alpha.lan` (10.100.0.160) and 10.100.0.161 had no ARP reply, and the IPv6 link-local fallback cannot work while xsm is on Wi-Fi. When it is back, the first `just bb` bootstraps it.
- **x-tall** (12 cores, 15 GiB): the working default. Proof runs below used it.
- **x-p35** (8 cores) and **x-19** (no ssh config entry; does not resolve) follow.

Use `BB_JOBS=<n>` to change the Go `-p` cap.

## Lanes, locking and isolation

```
~/bb-src/vecnet4/
├── .toolchain/   go-1.27.1, node-v22.23.3, uv, py venv, playwright-1.61.1, ms-playwright
├── .cache/       go-build, gomod, npm, uv (shared; safe for concurrent use)
└── <lane>/
    ├── src/      synced worktree (rsync --delete confined here)
    ├── home/     lane HOME
    ├── tmp/      TMPDIR + TMUX_TMPDIR; demo builds, data dirs, tmux sockets
    ├── shots/    remote SHOTS_DIR, emptied before each run
    └── owner     host:path of the worktree that owns the lane
```

- One run per lane at a time: a local `flock` (`$XDG_RUNTIME_DIR/vecnet-bb/<host>-<lane>.lock`) serialises runs from the same worktree, so a second sync never rewrites a tree under a running command. Different worktrees use different lanes and run in parallel.
- A lane remembers its owner and refuses another checkout with the same basename. Set `BB_LANE=<name>` to pick a different lane.
- The screen scripts' tmux servers (`-L oui-<pid>`), demo hubs (`127.0.0.1:0`), build dirs and `reap-demos.sh` (`REAP_ROOTS=$TMPDIR`) all stay inside `<lane>/tmp`. Two lanes ran screen scripts on x-tall at the same time without touching each other.
- If the ssh session ends (Ctrl-C, dropped link), `remote.sh` notices it has been reparented and stops the command's process group.

## Sync rules

Sent: the worktree minus `.git`, `node_modules/`, `.worktrees/`, `.data/`, `.demo-data/`, `.scratch/`, `bin/`, `backups/`, `__pycache__`, and secret-shaped files (`.env*`, `*.pem`, `*.key`, `auth.json`, `.netrc`, `credentials*`). No test needs the source's `.git`: the hook-install tests `git init` their own repositories in `t.TempDir()`. Box-side `node_modules` (from `just ln-setup`) and `bin/` survive between runs.

Environment forwarded: `CGO_ENABLED CC CXX GOFLAGS GORACE GOEXPERIMENT GODEBUG UPDATE_GOLDEN SHOTS_DIR REAP_MIN_AGE BB_JOBS BB_TIMEOUT NODE_OPTIONS`, plus names listed in `BB_ENV`. Names containing KEY, TOKEN, SECRET, PASS, CRED, AUTH, COOKIE or SESSION are refused. Nothing from `~/.config/vecnet`, no LLMP key, no ssh agent (`-a`).

## Coming back

- **SHOTS_DIR**: a local path (relative paths resolve from the worktree root, as they would locally) becomes `<lane>/shots` on the box; afterwards its contents are copied to the local path. It is not emptied locally.
- **Files the command wrote** (goldens with `UPDATE_GOLDEN=1`, committed shots from `just ui-shots` without `SHOTS_DIR`, a `go.sum`): pulled back when their content differs, unless the local copy changed during the run (it is then left alone, with a message). `BB_PULLBACK=0` turns this off. Box shots use the same Inter and JetBrains Mono as xsm, so layout and line breaks match. The pixels still differ, because the box has different font rasterisation and fallback fonts (DejaVu, where xsm has Noto). Regenerating the committed PNGs on the box therefore rewrites every file, so give checks an explicit `SHOTS_DIR`, and decide deliberately when committed shots should come from the box.

## First-run bootstrap

`scripts/bb/bootstrap.sh` runs on every call. Once the toolchain exists, it only checks stamps. The first time on a box it downloads Go, Node, uv and the two fonts (checksums pinned in the script), builds the Python venv, runs `npm ci` for Playwright, and installs the chromium headless shell. That took about a minute on x-tall, under a lock so that concurrent lanes do not race. It needs `curl tar xz sha256sum tmux` on the box. It warns when `just` or `gcc` is missing. It never uses sudo or installs packages.

To upgrade a pin, change the version and checksum in `bootstrap.sh` (or the lockfile in `scripts/bb/playwright/`). The next run installs the new version beside the old one.

## Cleanup

```sh
just bb-clean <lane>          # removes ~/bb-src/vecnet4/<lane> on every reachable box
BB_HOST=x-tall just bb-clean <lane>
```

It removes only that full path, and it refuses while any process has its cwd inside the lane. The shared `.toolchain` and `.cache` are never removed by a recipe. Remove them by hand, by full path, only when no lane is running.

## Timings (x-tall, 2026-11-04)

| Command | Run time on the box | Notes |
|---|---|---|
| first-run bootstrap | 58 s, plus 8 s for fonts | once per box |
| `go test ./...` | 182 s cold (module download + compile), 52 s warm with `-count=1` | `-p 8` |
| `CGO_ENABLED=1 CC=/usr/bin/gcc go test -race -count=1 ./internal/store/ ./internal/hub/` | 204 s | |
| `just ui-shots` | 247 s; 93 PNGs | the pull of 93 PNGs over Wi-Fi adds 7–13 s |
| `build2 node --test --test-concurrency=2 internal/ui/jstest/` | 1.9 s | 141 tests |
| `just validate-protocol` | 1.4 s | runs the jsonschema example check |
| wrapper overhead, warm lane | 1–1.6 s | the first sync of a new lane takes about 10 s |

Known flake: `scripts/ui/tui-subs.mjs:69` (the `expired … expired` row after `advance('3m')`) failed in 2 of 3 full `ui-shots` runs on x-tall. It passed in all 6 runs on its own.
