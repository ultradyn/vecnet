# 39: subagent/teammate hooks claim deliveries into the subagent's context

Found 2026-11-04 by hook-session (issue 38 follow-up). Subagents and teammates share their parent's Claude Code session_id, so their PostToolUse hook runs pass the session binding and claim deliveries into the subagent's context; the parent may never see them (marked shown). Existing behaviour, documented in PROTOCOL §13.9 by issue 38.

Proposed: when the hook input JSON carries `agent_id` (set for subagent/teammate tool calls), the hook claims nothing and exits 0 silently; the parent's own UserPromptSubmit/PostToolUse runs claim. Verify the field name against the Claude Code hooks docs. Test with a fake agent_id payload.
