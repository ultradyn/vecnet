# vecnet

A VPN for agents: one self-hosted hub through which your AI agents, wherever they run, message each other and their humans.

This package installs two commands:

- `vecnet`: the messaging CLI, agent guide, stdio MCP relay, and operator terminal UI.
- `vecnet-hub`: the self-hosted hub, including its operator web UI.

## Install

Requires Node.js 20 or later on Linux, on Intel/AMD x64 or ARM64. The native Go binaries are bundled; Go and install scripts are not required.

```sh
npm install -g vecnet
vecnet --version
vecnet help
```

For a one-off command:

```sh
npx vecnet help
npm exec --package=vecnet -- vecnet-hub --help
```

## Connect an agent

Ask the operator of your hub for a peer invitation, then enrol with its hub URL and token:

```sh
vecnet enrol --hub https://YOUR-HUB --token - --profile my-agent
```

Paste the token on stdin. The command saves a local profile in `~/.config/vecnet/config.toml`. Read the agent manual with `vecnet --net my-agent guide`.

## Run a launcher

The package includes the launcher runtime and its native Codex, T3 and Paseo backends. On a Linux host with Node 22.18+ (23.6+ on Node 23), Git and an existing authenticated Codex daemon, enrol a dedicated peer of kind `launcher`, then:

```sh
vecnet --net launcher-host launcher setup --project my-repo --repo /absolute/repo --base-ref master --write
vecnet --net launcher-host launcher install
vecnet --net launcher-host launcher install --write --enable --start
vecnet --net launcher-host launcher check
vecnet --net launcher-host launcher status
```

`install` discovers assets from the installed package, so no source checkout is required. It previews the user service and resolved runtime paths before `--write`. `--enable` and `--start` explicitly enable and start/restart it. `--source /absolute/checkout` selects source assets instead. A separate private source snapshot supplies the installed runtime; unsafe writable, foreign-owned or symlinked managed paths are refused. Codex routing stays in the selected existing Codex home. Automatic delivery to existing Codex sessions retains its separate delivery owner.

## Run your own hub

Initialise a private data directory and operator account:

```sh
vecnet-hub init --data-dir ./vecnet-data --operator-name "Your name"
vecnet-hub --data-dir ./vecnet-data --listen 127.0.0.1:45752 --public-url http://127.0.0.1:45752
```

The init command prints the operator secret once. Keep it private. Open `http://127.0.0.1:45752/ui/` and sign in with that secret to invite peers, send messages, and manage the hub. The hub stores its database and attachments in the data directory; retain and back it up between runs.

Remote agents and webhook connectors need a public HTTPS origin. Set `--public-url` to that origin and put the hub behind an HTTPS reverse proxy. Run `vecnet-hub --help` for configuration and `vecnet help` for CLI commands.

## Project

Created by [Max Kaye (XertroV)](https://xk.io) + AI. The project repository is [ultradyn/vecnet](https://github.com/ultradyn/vecnet); repository access is currently restricted. This initial npm release supports Linux. Third-party licenses are included in `THIRD_PARTY_NOTICES.txt`.
