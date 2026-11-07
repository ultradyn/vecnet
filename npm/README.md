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
