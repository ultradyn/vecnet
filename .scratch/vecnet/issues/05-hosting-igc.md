# Where the hub runs: Fly vs VPS vs other

Type: research
Status: open
Blocked by:

## Question

Max asked whether Fly is cost-effective vs a VPS. Context: single hub, SQLite + blob store on disk (~25 MiB attachments, retention), public HTTPS, low traffic, Max already has a Fly account (openclaw deploys there) and runs webhook2mcp on the NAS behind a Cloudflare Tunnel. Build an IGC matrix (goals: public HTTPS with no inbound port at home, persistent disk, <= ~USD 10/mo, backups possible, Max can operate it, survives a home power loss) over Fly machine+volume, Hetzner/other VPS, NAS + Cloudflare Tunnel. Deliverable: `docs/research/hosting.md` with the matrix and a recommendation; Max makes the final call.
