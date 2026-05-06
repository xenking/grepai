---
name: grepai
description: Use when configuring grepai for a project, exposing grepai to Codex via MCP, or using semantic code search/call graph tools. Prefer FFF/fffq for exact file and text lookup; use grepai for intent-based semantic search and code relationship exploration.
---

## Scope

Use this skill for:

- adding a repository to grepai from scratch;
- configuring `.grepai/config.yaml` and ignore patterns;
- starting persistent grepai watchers;
- exposing grepai to Codex as MCP tools;
- semantic code search by intent;
- call graph and reference exploration.

Do **not** use grepai as the first tool for exact file names, exact symbols, or
literal text lookup when FFF/`fffq` is available. Use `fffq` first for exact
repo inspection, then grepai for semantic/intent search.

## Semantic Search

Use English natural-language queries for best embedding quality:

```bash
grepai search "user authentication flow" --json --compact -n 5
grepai search "database transaction error handling" --json --compact -n 8
grepai search "how websocket messages are routed" --json --compact
```

Use path filtering when the relevant area is known:

```bash
grepai search "token refresh logic" --path internal/auth --json --compact
```

## Call Graph and References

```bash
grepai trace callers "HandleRequest" --json
grepai trace callees "ProcessOrder" --json
grepai trace graph "ValidateToken" --depth 3 --json
grepai refs readers "CurrentUser" --json
grepai refs writers "CurrentUser" --json
```

## Adding a Project Correctly

Preferred local architecture:

1. one isolated grepai project per repository;
2. one persistent watcher per project;
3. one long-running Streamable HTTP MCP endpoint per project;
4. Codex connects to that endpoint by URL.

This avoids workspace leakage and prevents every Codex terminal/session from
spawning duplicate `grepai mcp-serve` subprocesses.

Do **not** default to a workspace. Workspaces are only for deliberate
cross-project search and require PostgreSQL or Qdrant; the `gob` backend is not
supported for workspaces.

### 1. Initialize

```bash
cd /absolute/path/to/project
grepai init --provider voyageai --model voyage-code-3 --backend gob --yes
```

Use `voyage-code-3` for best code retrieval quality. Use `voyage-4-lite` only
when latency/cost matters more than retrieval quality.

### 2. Configure ignores before indexing

Edit `.grepai/config.yaml` and exclude noisy/generated/secret-bearing paths:

```yaml
ignore:
  - .git
  - .grepai
  - .omx
  - .graphify
  - node_modules
  - vendor
  - dist
  - build
  - target
  - .venv
  - venv
  - data
  - "*.log"
  - ".env"
  - "secrets*.yaml"
```

Add project-specific generated binaries, dumps, large fixtures, local database
files, and secret files.

### 3. Build and maintain the index

Foreground one-off build/update:

```bash
grepai watch --no-ui
```

Persistent macOS watcher LaunchAgent shape:

```xml
<key>Label</key><string>com.xenking.grepai.PROJECT_ID</string>
<key>ProgramArguments</key>
<array>
  <string>/Users/xenking/.local/bin/grepai</string>
  <string>watch</string>
  <string>--no-ui</string>
  <string>--ready-timeout</string>
  <string>300s</string>
</array>
<key>WorkingDirectory</key><string>/absolute/path/to/project</string>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key>
<dict>
  <key>Crashed</key><true/>
  <key>SuccessfulExit</key><false/>
</dict>
```

Load and verify:

```bash
launchctl bootstrap "gui/$(id -u)" "$HOME/Library/LaunchAgents/com.xenking.grepai.PROJECT_ID.plist"
launchctl list | grep "com.xenking.grepai.PROJECT_ID"
grepai status --no-ui
```

Never put recursive `find ~/Projects ... .grepai` discovery in a frequent
status-bar script or cron job. The watcher already checks whether reindexing is
needed.

### 4. Expose MCP without per-session fanout

Avoid this in global Codex config for frequently used projects:

```toml
[mcp_servers.grepai_PROJECT_ID]
command = "/Users/xenking/.local/bin/grepai"
args = ["mcp-serve", "/absolute/path/to/project"]
```

That starts a new stdio `grepai mcp-serve` for every Codex session. Instead,
run one long-lived Streamable HTTP endpoint per project:

```bash
grepai mcp-serve /absolute/path/to/project \
  --transport streamable-http \
  --http-bind 127.0.0.1:PORT \
  --http-path /mcp
```

Persistent macOS MCP LaunchAgent shape:

```xml
<key>Label</key><string>com.xenking.grepai.mcp.PROJECT_ID</string>
<key>ProgramArguments</key>
<array>
  <string>/Users/xenking/.local/bin/grepai</string>
  <string>mcp-serve</string>
  <string>/absolute/path/to/project</string>
  <string>--transport</string>
  <string>streamable-http</string>
  <string>--http-bind</string>
  <string>127.0.0.1:PORT</string>
  <string>--http-path</string>
  <string>/mcp</string>
</array>
<key>WorkingDirectory</key><string>/absolute/path/to/project</string>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key>
<dict>
  <key>Crashed</key><true/>
  <key>SuccessfulExit</key><false/>
</dict>
```

Codex config:

```toml
[mcp_servers.grepai_PROJECT_ID]
url = "http://127.0.0.1:PORT/mcp"
```

### 5. Verify end to end

```bash
cd /absolute/path/to/project
grepai status --no-ui
grepai search "main business flow" --json --compact -n 5
launchctl list | grep "com.xenking.grepai.PROJECT_ID"
launchctl list | grep "com.xenking.grepai.mcp.PROJECT_ID"
lsof -nP -iTCP:PORT -sTCP:LISTEN
codex mcp list
codex debug prompt-input "ping"
ps -axww -o command= | grep "grepai mcp-serve /absolute/path/to/project"
```

Expected process shape:

- one `grepai watch --no-ui` per indexed project;
- one `grepai mcp-serve ... --transport streamable-http` per MCP-exposed project;
- zero duplicate stdio `grepai mcp-serve` children per Codex session.

## Fallback

If grepai fails:

- run `grepai status --no-ui`;
- check the watcher/MCP LaunchAgents with `launchctl list`;
- inspect logs under `~/Library/Logs/grepai`;
- fall back to `fffq` or shell search only after reporting grepai is unavailable.
