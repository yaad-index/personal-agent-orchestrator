package mcpsrv

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
)

// AgentInfo is one agent as a tool reports it.
type AgentInfo struct {
	Name    string   `json:"name" jsonschema:"the agent's name"`
	Tags    []string `json:"tags" jsonschema:"the tags the agent carries"`
	Backend string   `json:"backend" jsonschema:"the coding-agent harness that runs it"`
	State   string   `json:"state" jsonschema:"running or stopped"`
	// Temporary agents also say where they came from and how they are doing.
	Temporary bool   `json:"temporary" jsonschema:"whether it is a temporary agent"`
	Recipe    string `json:"recipe,omitempty" jsonschema:"the recipe a temporary agent was spawned from"`
	Channel   string `json:"channel,omitempty" jsonschema:"the channel a temporary agent's stream is bound to"`
	Flag      string `json:"flag,omitempty" jsonschema:"a temporary agent's status: active, idle or unused"`
}

// RecipeInfo is one recipe as a tool reports it.
type RecipeInfo struct {
	Name        string `json:"name" jsonschema:"the recipe's name"`
	Description string `json:"description" jsonschema:"what the recipe is for"`
}

// Operations is what the tools call. It is the node's existing operations, seen
// from the MCP surface; the daemon supplies it.
type Operations interface {
	// Agents lists every registered agent, configured and temporary.
	Agents() []AgentInfo
	// Recipes lists the installed recipes.
	Recipes() ([]RecipeInfo, error)
}

type noInput struct{}

type listAgentsOutput struct {
	Agents []AgentInfo `json:"agents"`
}

type listRecipesOutput struct {
	Recipes []RecipeInfo `json:"recipes"`
}

func (s *Server) addTools() {
	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "list_agents",
		Description: "List the agents pao runs: configured and temporary, with name, tags, state and, for a temporary agent, its recipe, channel and status flag. Only agents you may see are listed.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, s.listAgents)
	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "list_recipes",
		Description: "List the recipes a temporary agent can be spawned from.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, s.listRecipes)
}

// listAgents lists only the agents the caller may see: an agent the caller may
// not see is left out, as if it did not exist.
func (s *Server) listAgents(_ context.Context, req *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, listAgentsOutput, error) {
	who := subject(req)
	out := listAgentsOutput{Agents: []AgentInfo{}}
	for _, a := range s.ops.Agents() {
		if access.Allowed(s.auth, who, access.List, access.Resource{Agent: a.Name, Tags: a.Tags}) {
			out.Agents = append(out.Agents, a)
		}
	}
	return nil, out, nil
}

// listRecipes lists the recipes to a caller allowed to see them, and none to
// anyone else.
func (s *Server) listRecipes(_ context.Context, req *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, listRecipesOutput, error) {
	out := listRecipesOutput{Recipes: []RecipeInfo{}}
	if !access.Allowed(s.auth, subject(req), access.ListRecipes, access.Resource{}) {
		return nil, out, nil
	}
	rs, err := s.ops.Recipes()
	if err != nil {
		return nil, out, err
	}
	out.Recipes = append(out.Recipes, rs...)
	return nil, out, nil
}
