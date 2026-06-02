# AGENTS.md instructions for /Users/noam/work/skyvalley/gitrdone

<INSTRUCTIONS>
<!-- BEGIN COMPOUND CODEX TOOL MAP -->
## Compound Codex Tool Mapping (Claude Compatibility)

This section maps Claude Code plugin tool references to Codex behavior.
Only this block is managed automatically.

Tool mapping:
- Read: use shell reads (cat/sed) or rg
- Write: create files via shell redirection or apply_patch
- Edit/MultiEdit: use apply_patch
- Bash: use shell_command
- Grep: use rg (fallback: grep)
- Glob: use rg --files or find
- LS: use ls via shell_command
- WebFetch/WebSearch: use curl or Context7 for library docs
- AskUserQuestion/Question: present choices as a numbered list in chat and wait for a reply number. For multi-select (multiSelect: true), accept comma-separated numbers. Never skip or auto-configure — always wait for the user's response before proceeding.
- Task/Subagent/Parallel: run sequentially in main thread; use multi_tool_use.parallel for tool calls
- TodoWrite/TodoRead: use file-based todos in todos/ with file-todos skill
- Skill: open the referenced SKILL.md and follow it
- ExitPlanMode: ignore
<!-- END COMPOUND CODEX TOOL MAP -->

## Project Guidance

- gitrdone is a generic Git artifact service. Keep Differ domain concepts such as divergences, recommendations, and runtime users out of its core model unless they are opaque caller metadata.
- Control API identity is the repo ID. Repo names are labels and Git-facing locator components, not canonical control identifiers.
- Keep HTTP handlers thin over small interfaces. Put lifecycle behavior in repository/service implementations and keep request/response mapping in handlers.
- Work red-green for implementation changes: add or update focused tests first, confirm the failure, then implement the smallest change that makes them pass.
- Before making code changes, briefly state the implementation intention.
- Tests should stage repo data through setup helpers, the store, or the HTTP API. Do not depend on magic generated IDs such as `repo_123` unless a test is explicitly about route shape.
- API error bodies should be useful but bounded. Do not expose implementation internals, token material, or security-relevant detail.
</INSTRUCTIONS>
