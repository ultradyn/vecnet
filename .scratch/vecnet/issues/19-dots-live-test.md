# Live test: a real dot receives an MCP event

Type: task
Status: open
Blocked by:

## Question

Research (ticket 02) could not confirm from documentation that a dot can use a custom, unpublished MCP server plugin, nor what a pushed event looks like on the dot's side, nor whether write-tool confirmations block an unattended reply. webhook2mcp already serves the MCP Events profile live at `https://gb2cxc.xpp.exchange`, so Max can test without new code. Checklist for Max: (1) confirm whether "Mira" is a dot or a Work chat; (2) with a real dot, install the webhook2mcp plugin and ask it to subscribe to an event; (3) trigger one event and record where it lands and whether the dot acts unattended; (4) have the dot call a write tool and record whether a confirmation prompt blocks it. Record the facts in `docs/research/dots.md` under "Live test". The dots connector design (ticket 08 onwards) is provisional until this resolves.
