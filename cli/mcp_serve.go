package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/yoanbernabeu/grepai/config"
	"github.com/yoanbernabeu/grepai/mcp"
)

var mcpServeCmd = &cobra.Command{
	Use:   "mcp-serve [project-path]",
	Short: "Start grepai as an MCP server",
	Long: `Start grepai as an MCP (Model Context Protocol) server.

This allows AI agents to use grepai as a native tool through the MCP protocol.
The server communicates via stdio by default, or Streamable HTTP when
--transport=streamable-http is selected. It exposes the following tools:

  - grepai_search: Semantic code search with natural language (includes RPG context when enabled)
  - grepai_trace_callers: Find all functions that call a symbol
  - grepai_trace_callees: Find all functions called by a symbol
  - grepai_trace_graph: Build a call graph around a symbol
  - grepai_refs_readers: Find property/state readers for a symbol name
  - grepai_refs_writers: Find property/state writers for a symbol name
  - grepai_refs_graph: Build a property usage graph (readers + writers)
  - grepai_index_status: Check index health and statistics (includes RPG stats when enabled)

The following tools are only advertised when RPG is enabled in .grepai/config.yaml:

  - grepai_rpg_search: Search RPG graph nodes by feature semantics
  - grepai_rpg_fetch: Fetch hierarchy and edge context for a specific RPG node
  - grepai_rpg_explore: Traverse RPG graph neighborhoods with direction/depth filters

Arguments:
  project-path  Optional path to the grepai project directory.
                If not provided, searches for .grepai from current directory.

Flags:
  --workspace   Workspace name. When set, serves using workspace config from
                ~/.grepai/workspace.yaml without requiring local .grepai/.
  --transport   MCP transport: stdio (default) or streamable-http.
  --http-bind   Streamable HTTP listen address (default 127.0.0.1:8762).
  --http-path   Streamable HTTP endpoint path (default /mcp).

Configuration for Claude Code:
  claude mcp add grepai -- grepai mcp-serve
  claude mcp add grepai -- grepai mcp-serve --workspace myworkspace

Configuration for Cursor (.cursor/mcp.json):
  {
    "mcpServers": {
      "grepai": {
        "command": "grepai",
        "args": ["mcp-serve"]
      }
    }
  }

Configuration for Cursor with explicit path (recommended for Windows):
  {
    "mcpServers": {
      "grepai": {
        "command": "grepai",
        "args": ["mcp-serve", "/path/to/your/project"]
      }
    }
  }`,
	Args: cobra.MaximumNArgs(1),
	RunE: runMCPServe,
}

const (
	mcpTransportStdio          = "stdio"
	mcpTransportStreamableHTTP = "streamable-http"
)

func init() {
	mcpServeCmd.Flags().String("workspace", "", "Workspace name for workspace-only mode (no local .grepai/ required)")
	mcpServeCmd.Flags().String("transport", mcpTransportStdio, "MCP transport: stdio or streamable-http")
	mcpServeCmd.Flags().String("http-bind", mcp.DefaultStreamableHTTPOptions().Bind, "Streamable HTTP listen address")
	mcpServeCmd.Flags().String("http-path", mcp.DefaultStreamableHTTPOptions().EndpointPath, "Streamable HTTP MCP endpoint path")
	mcpServeCmd.Flags().StringSlice("http-allow-origin", nil, "Additional allowed Origin header for Streamable HTTP (repeatable)")
	mcpServeCmd.Flags().Bool("http-stateless", false, "Use stateless Streamable HTTP sessions")
	rootCmd.AddCommand(mcpServeCmd)
}

func validateMCPTransport(transport string) error {
	switch transport {
	case mcpTransportStdio, mcpTransportStreamableHTTP:
		return nil
	default:
		return fmt.Errorf("unsupported MCP transport %q (expected %q or %q)", transport, mcpTransportStdio, mcpTransportStreamableHTTP)
	}
}

// resolveMCPTarget determines the project root and/or workspace for the MCP server.
// Returns (projectRoot, workspaceName, error).
// projectRoot may be empty when in workspace-only mode.
func resolveMCPTarget(explicitPath, workspaceName string) (string, string, error) {
	// Priority 1: Explicit --workspace flag
	if workspaceName != "" {
		cfg, err := config.LoadWorkspaceConfig()
		if err != nil {
			return "", "", fmt.Errorf("failed to load workspace config: %w", err)
		}
		if cfg == nil {
			return "", "", fmt.Errorf("no workspace config found at ~/.grepai/workspace.yaml")
		}
		if _, err := cfg.GetWorkspace(workspaceName); err != nil {
			return "", "", fmt.Errorf("workspace %q not found", workspaceName)
		}

		// Check if cwd has local config (optional, for trace tools)
		projectRoot := ""
		if pr, err := config.FindProjectRoot(); err == nil {
			projectRoot = pr
		}

		return projectRoot, workspaceName, nil
	}

	// Priority 2: Explicit project path argument
	if explicitPath != "" {
		if !filepath.IsAbs(explicitPath) {
			abs, err := filepath.Abs(explicitPath)
			if err != nil {
				return "", "", fmt.Errorf("failed to resolve path: %w", err)
			}
			explicitPath = abs
		}
		if !config.Exists(explicitPath) {
			return "", "", fmt.Errorf("no grepai project found at %s (run 'grepai init' first)", explicitPath)
		}
		return explicitPath, "", nil
	}

	// Priority 3: FindProjectRoot (walk upward from cwd)
	projectRoot, err := config.FindProjectRoot()
	if err == nil {
		return projectRoot, "", nil
	}

	// Priority 4: Auto-detect workspace from cwd
	cwd, cwdErr := os.Getwd()
	if cwdErr != nil {
		return "", "", fmt.Errorf("failed to find project root: %w", err)
	}

	wsName, ws, wsErr := config.FindWorkspaceForPath(cwd)
	if wsErr != nil {
		// If workspace config exists with at least one workspace, allow starting
		// unscoped MCP server and let tools accept workspace at runtime.
		cfg, cfgErr := config.LoadWorkspaceConfig()
		if cfgErr == nil && cfg != nil && len(cfg.Workspaces) > 0 {
			return "", "", nil
		}
		return "", "", fmt.Errorf("no grepai project or workspace found (run 'grepai init' or use --workspace)")
	}
	if ws != nil {
		return "", wsName, nil
	}

	// No containing workspace for cwd, but still allow startup if global
	// workspace config has entries (runtime workspace argument can be used).
	cfg, cfgErr := config.LoadWorkspaceConfig()
	if cfgErr == nil && cfg != nil && len(cfg.Workspaces) > 0 {
		return "", "", nil
	}

	return "", "", fmt.Errorf("no grepai project or workspace found (run 'grepai init' or use --workspace)")
}

// resolveRPGEnabled reports whether the RPG feature is enabled for the given
// project root. When projectRoot is empty (workspace-only mode without a local
// project) or the config cannot be loaded, it returns false so that RPG tools
// are not advertised.
func resolveRPGEnabled(projectRoot string) bool {
	if projectRoot == "" {
		return false
	}
	cfg, err := config.Load(projectRoot)
	if err != nil {
		return false
	}
	return cfg.RPG.Enabled
}

func runMCPServe(cmd *cobra.Command, args []string) error {
	workspaceFlag, _ := cmd.Flags().GetString("workspace")
	transport, _ := cmd.Flags().GetString("transport")
	if err := validateMCPTransport(transport); err != nil {
		return err
	}

	var explicitPath string
	if len(args) > 0 {
		explicitPath = args[0]
	}

	projectRoot, wsName, err := resolveMCPTarget(explicitPath, workspaceFlag)
	if err != nil {
		return err
	}

	rpgEnabled := resolveRPGEnabled(projectRoot)

	var srv *mcp.Server
	if wsName != "" {
		srv, err = mcp.NewServerWithWorkspace(projectRoot, wsName, rpgEnabled)
	} else {
		srv, err = mcp.NewServer(projectRoot, rpgEnabled)
	}
	if err != nil {
		return fmt.Errorf("failed to create MCP server: %w", err)
	}

	if transport == mcpTransportStreamableHTTP {
		httpBind, _ := cmd.Flags().GetString("http-bind")
		httpPath, _ := cmd.Flags().GetString("http-path")
		allowedOrigins, _ := cmd.Flags().GetStringSlice("http-allow-origin")
		httpStateless, _ := cmd.Flags().GetBool("http-stateless")
		return srv.ServeStreamableHTTP(mcp.StreamableHTTPOptions{
			Bind:           httpBind,
			EndpointPath:   httpPath,
			AllowedOrigins: allowedOrigins,
			Stateful:       !httpStateless,
		})
	}

	return srv.Serve()
}
