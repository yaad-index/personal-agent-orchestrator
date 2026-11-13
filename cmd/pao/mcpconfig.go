package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// McpConfigCmd prints the backend's MCP configuration that points an agent at
// its node's pao server, for a profile's startup command (for example:
// claude --mcp-config "$(pao mcp-config)"). It carries no secret: the address
// and the agent's own token are read from the environment the node started the
// agent with, when the backend starts.
type McpConfigCmd struct {
	Name string `help:"Name the server is given in the backend." default:"pao"`
}

// Run implements mcp-config.
func (c McpConfigCmd) Run() error { return c.run(os.Stdout) }

func (c McpConfigCmd) run(w io.Writer) error {
	cfg := map[string]any{"mcpServers": map[string]any{
		c.Name: map[string]any{
			"type": "http",
			"url":  "${" + envMCPURL + "}",
			"headers": map[string]string{
				"Authorization": "Bearer ${" + envAgentToken + "}",
			},
		},
	}}
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}
