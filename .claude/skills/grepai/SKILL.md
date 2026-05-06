---
name: grepai
description: "Use for grepai semantic code search, call graph/reference exploration, and project setup. Prefer grepai for intent-based code questions such as where behavior lives or how a flow works; use exact-match tools for literal file names, symbols, imports, and strings."
---

## Semantic Code Exploration

Use grepai for semantic code exploration and code relationship lookup. It does
not replace exact-match tools for literal strings, imports, file names, or known
symbols.

**WRONG**:
- Using built-in `Grep` to find "where authentication happens"
- Using built-in `Glob` to explore "error handling code"
- Searching by intent with regex patterns

**CORRECT**:
- Invoke this skill, then use `grepai search "authentication flow"` for semantic search
- Invoke this skill, then use `grepai trace callers "FunctionName"` for call graph
- Use exact-match tools only for exact text matches (variable names, imports)

## When to Invoke This Skill

Invoke this skill **IMMEDIATELY** when:

- User asks to find code by **intent** (e.g., "where is authentication handled?")
- User asks to understand **what code does** (e.g., "how does the indexer work?")
- User asks to explore **functionality** (e.g., "find error handling logic")
- You need to understand **code relationships** (e.g., "what calls this function?")
- User asks about **implementation details** (e.g., "how are vectors stored?")

Do not use exact-match tools for intent-based searches. Use grepai instead.

## When to Use Exact-Match Tools

Use exact-match tools for:

- Exact text matching: `Grep "func NewIndexer"` (find exact function name)
- Specific imports: `Grep "import.*cobra"` (find import statements)
- File patterns: `Glob "**/*.go"` (find files by extension)
- Variable references: `Grep "configPath"` (find exact variable name)

## How to Use This Skill

### Semantic Search

Use `grepai search` to find code by **describing what it does**:

```bash
# Search with natural language (ALWAYS use English for best results)
grepai search "user authentication flow"
grepai search "error handling middleware"
grepai search "database connection pooling"
grepai search "API request validation"

# JSON output for AI agents (--compact saves ~80% tokens)
grepai search "authentication flow" --json --compact

# Limit results
grepai search "error handling" -n 5
```

### Call Graph Tracing

Use `grepai trace` to understand **function relationships**:

```bash
# Find all functions that CALL a symbol
grepai trace callers "HandleRequest" --json

# Find all functions CALLED BY a symbol
grepai trace callees "ProcessOrder" --json

# Build complete call graph (both directions)
grepai trace graph "ValidateToken" --depth 3 --json
```

### Query Best Practices

**Do:**
```bash
grepai search "How are file chunks created and stored?"
grepai search "Vector embedding generation process"
grepai search "Configuration loading and validation"
grepai trace callers "Search" --json
```

**Don't:**
```bash
grepai search "func"           # Too vague
grepai search "error"          # Too generic
grepai search "HandleRequest"  # Use Grep for exact matches
```

## Recommended Workflow

1. **Start with `grepai search`** to find relevant code semantically
2. **Use `grepai trace`** to understand function relationships
3. **Use `Read` tool** to examine files from search results
4. **Use `Grep`** only for exact string searches if needed

## Adding a Project Correctly

Use this runbook when adding a new local project to grepai. The default,
preferred shape is **one isolated grepai project**, **one persistent watcher**,
and **one long-running HTTP MCP endpoint**. Do **not** default to a workspace
unless the user explicitly wants cross-project search and accepts a shared
PostgreSQL/Qdrant backend.

### 1. Initialize the project

```bash
cd /absolute/path/to/project
grepai init --provider voyageai --model voyage-code-3 --backend gob --yes
```

Use `gob` for normal single-user/local projects: it is file-based, isolated,
simple to back up, and avoids exposing unrelated or unindexed projects through a
workspace. Use workspaces only for deliberate multi-project search; workspaces
require PostgreSQL or Qdrant because `gob` is not supported there.

For latency/cost-sensitive projects, `voyage-4-lite` is acceptable. For best
code retrieval quality, prefer `voyage-code-3`.

### 2. Configure ignore patterns

Edit `.grepai/config.yaml` before the first full index. Always exclude noisy or
secret-bearing paths, for example:

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

Keep project-specific generated binaries, dumps, fixtures, and local secret
files out of the index.

### 3. Build and maintain the index

For a one-off foreground build:

```bash
grepai watch --no-ui
```

For a persistent macOS watcher, create a LaunchAgent with a project-specific
label and `WorkingDirectory`:

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

Then load it:

```bash
launchctl bootstrap "gui/$(id -u)" "$HOME/Library/LaunchAgents/com.xenking.grepai.PROJECT_ID.plist"
launchctl list | grep "com.xenking.grepai.PROJECT_ID"
```

Do not put recursive `find ~/Projects ...` discovery into status-bar scripts or
frequent cron jobs. The watcher already decides whether a reindex is needed.

### 4. Expose MCP without per-session process fanout

Avoid configuring every AI terminal to spawn its own stdio process:

```toml
# Avoid this for frequently used/global projects:
command = "/Users/xenking/.local/bin/grepai"
args = ["mcp-serve", "/absolute/path/to/project"]
```

Instead, run exactly one long-lived MCP endpoint per project:

```bash
grepai mcp-serve /absolute/path/to/project \
  --transport streamable-http \
  --http-bind 127.0.0.1:PORT \
  --http-path /mcp
```

On macOS, make it a LaunchAgent with a distinct label:

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

Then configure Codex/Claude-compatible MCP clients to connect by URL:

```toml
[mcp_servers.grepai_PROJECT_ID]
url = "http://127.0.0.1:PORT/mcp"
```

This keeps project isolation while preventing dozens of duplicate
`grepai mcp-serve` children across Codex/Claude/OMX sessions.

### 5. Verify the project end to end

```bash
cd /absolute/path/to/project
grepai status --no-ui
grepai search "main business flow" --json --compact -n 5
launchctl list | grep "com.xenking.grepai.PROJECT_ID"
launchctl list | grep "com.xenking.grepai.mcp.PROJECT_ID"
lsof -nP -iTCP:PORT -sTCP:LISTEN
```

For Codex, verify the MCP config and that no stdio grepai MCP process is
spawned for new sessions:

```bash
codex mcp list
codex debug prompt-input "ping"
ps -axww -o command= | grep "grepai mcp-serve /absolute/path/to/project"
```

Expected result: one watcher process, one HTTP MCP process, no duplicate stdio
MCP processes per terminal/session.

## Fallback

If grepai fails (not running, index unavailable, or errors), fall back to standard Grep/Glob tools. Common issues:

- Index not built: Run `grepai watch` to build/update the index
- Embedder not available: Check that Ollama is running or OpenAI API key is set

## Keywords

semantic search, code search, natural language search, find code, explore codebase,
call graph, callers, callees, function relationships, code understanding,
intent search, grep replacement, code exploration
