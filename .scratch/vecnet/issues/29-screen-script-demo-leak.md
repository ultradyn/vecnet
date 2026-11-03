# 29: Screen scripts leak demo hubs

Found 2026-11-03 while checking agent-build.slice, which was at its 12G MemoryMax (reported by cc-pagchat-cos). Ten `/tmp/vecnet-ui-build-*/demo` processes had been orphaned (PPID = systemd user), aged 2 h to 22 h, their temp dirs already deleted. They come from `scripts/ui/*.mjs` runs (bootDemo/withDemo in `scripts/ui/lib.mjs`) whose node process died or was killed, e.g. by a gate timeout or an OOM kill, before teardown ran.

Each one was small (~16 MB RSS). Most of the slice's memory was pag-chat test temp dirs in /dev/shm (told them), but the leak is real.

## Fix
- `bootDemo` starts the demo in its own process group and kills that group on exit, SIGINT, SIGTERM and uncaught errors.
- As a backstop, the demo binary gets a `--parent-pid` (or `--exit-with-stdin`) option that makes it exit when its parent goes away. Closing stdin works without a new flag if the demo exits on EOF.
- `just ui-shots` reaps leftover `vecnet-ui-build-*/demo` processes older than 2 h before it starts.
- Test: kill -9 a screen script mid-run; within a few seconds no demo is left.
