# 40: local-scope MCP relay is visible to every session in the directory

Found 2026-11-04 amending the MCP-first plan. `hook install --mcp` registers `vecnet-<P>` with `claude mcp add --scope local` from the session start dir, so every Claude Code session started there sees every installed profile's relay tools, including sessions for another profile. The relay refuses only when VECNET_PROFILE is set and differs; the installer warns. Same class as issue 38.

Options: the relay checks CLAUDE_CODE_SESSION_ID against the profile's hook binding (state/<p>+session.json) and refuses a foreign session; or register per-session. Decide after MCP-first lands and Task 11 verifies local-scope keying live.
