package mcpsrv

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
)

// AgentInfo is one agent as a tool reports it.
type AgentInfo struct {
	Name    string   `json:"name" jsonschema:"the agent's name"`
	Tags    []string `json:"tags" jsonschema:"the tags the agent carries"`
	Backend string   `json:"backend" jsonschema:"the coding-agent harness that runs it"`
	Node    string   `json:"node,omitempty" jsonschema:"the node that hosts it, when it is not this one"`
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

// KillInfo is what a kill reports.
type KillInfo struct {
	HarvestID   string `json:"harvest_id" jsonschema:"the harvest the agent's outputs were written to"`
	Complete    bool   `json:"complete" jsonschema:"false if a limit stopped the copy"`
	WorkdirKept bool   `json:"workdir_kept" jsonschema:"true if the agent's files were not destroyed because the harvest was incomplete"`
	Files       int    `json:"files" jsonschema:"how many files were harvested"`
	Bytes       int64  `json:"bytes" jsonschema:"how many bytes were harvested"`
	Summary     string `json:"summary" jsonschema:"a readable report of what was harvested"`
}

// FileInfo is one harvested file.
type FileInfo struct {
	Path string `json:"path" jsonschema:"path inside the harvest"`
	Size int64  `json:"size" jsonschema:"size in bytes"`
}

// HarvestInfo describes one harvest.
type HarvestInfo struct {
	ID        string     `json:"id" jsonschema:"the harvest's id"`
	Agent     string     `json:"agent" jsonschema:"the agent whose outputs it holds"`
	Tags      []string   `json:"tags" jsonschema:"the agent's tags when it was killed"`
	Requester string     `json:"requester" jsonschema:"who asked for the kill"`
	Time      string     `json:"time" jsonschema:"when it was harvested, RFC 3339"`
	Complete  bool       `json:"complete" jsonschema:"false if a limit stopped the copy"`
	Bytes     int64      `json:"bytes" jsonschema:"total size of the harvested files"`
	Files     []FileInfo `json:"files,omitempty" jsonschema:"the harvested files; only when one harvest is read"`
}

// Operations is what the tools call. It is the node's existing operations, seen
// from the MCP surface; the daemon supplies it. Operations that act on behalf
// of a caller take its subject, and the operation decides what that subject may
// do, exactly as for the other surfaces: this package never decides for them.
type Operations interface {
	// Agents lists every registered agent, configured and temporary.
	Agents() []AgentInfo
	// Recipes lists the installed recipes.
	Recipes() ([]RecipeInfo, error)
	// Spawn creates a temporary agent and returns its name. When the agent was
	// created but did not become ready it returns the name with the error.
	Spawn(ctx context.Context, who access.Subject, recipe, name, channel string) (string, error)
	// Kill tears a temporary agent down, harvesting first.
	Kill(ctx context.Context, who access.Subject, name string) (KillInfo, error)
	// Harvests lists the harvests who may read.
	Harvests(who access.Subject) ([]HarvestInfo, error)
	// Harvest returns one harvest, with its files, if who may read it.
	Harvest(who access.Subject, id string) (HarvestInfo, error)
	// HarvestFile returns one harvested file if who may read the harvest.
	HarvestFile(who access.Subject, id, path string) ([]byte, error)
	// Send delivers a message to the agent named, or to every agent carrying
	// the tag, that who may address. The sender the recipients read is who, not
	// anything the caller supplies.
	Send(ctx context.Context, who access.Subject, byTag bool, target, text string) ([]Delivered, error)
}

// Delivered is the outcome for one addressed agent.
type Delivered struct {
	Agent    string `json:"agent" jsonschema:"the agent addressed"`
	Launched bool   `json:"launched" jsonschema:"whether it had to be started to receive the message"`
	Error    string `json:"error,omitempty" jsonschema:"why delivery to it failed, if it did"`
}

// maxFileBytes bounds what one read_harvest call returns.
const maxFileBytes = 1 << 20

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

	notDestructive := false
	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "send_message",
		Description: "Send a message to an agent by name, or to every agent carrying a tag. The recipients read it as `[you]: text` with your own name; you cannot choose another. You can only address agents you are allowed to; one you may not address is reported as not found.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive},
	}, s.sendMessage)

	destructive := true
	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "spawn_agent",
		Description: "Start a temporary agent from a recipe, in a fresh isolated working directory, with its stream bound to a channel. Returns its name. Spawning is gated: you may be asked to wait for the operator.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive},
	}, s.spawnAgent)
	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "kill_agent",
		Description: "Tear a temporary agent down. Its outputs are harvested first and the harvest id is returned; nothing the agent made is lost. Killing is gated: you may be asked to wait for the operator.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive},
	}, s.killAgent)
	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "list_harvests",
		Description: "List the harvests of killed temporary agents that you may read, newest first.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, s.listHarvests)
	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "read_harvest",
		Description: "Read a harvest. With only an id it lists the harvest's files; with a path it returns that file's content (text as is, anything else base64).",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, s.readHarvest)
}

type spawnInput struct {
	Recipe  string `json:"recipe" jsonschema:"the recipe to spawn from (see list_recipes)"`
	Name    string `json:"name,omitempty" jsonschema:"a name for the agent; generated from the recipe if left out"`
	Channel string `json:"channel,omitempty" jsonschema:"the channel its stream is bound to; the node's only channel if left out"`
}

type spawnOutput struct {
	Name string `json:"name" jsonschema:"the new agent's name"`
}

func (s *Server) spawnAgent(ctx context.Context, req *mcp.CallToolRequest, in spawnInput) (*mcp.CallToolResult, spawnOutput, error) {
	if in.Recipe == "" {
		return nil, spawnOutput{}, errors.New("a recipe is required")
	}
	name, err := s.ops.Spawn(ctx, subject(req), in.Recipe, in.Name, in.Channel)
	if err != nil {
		return nil, spawnOutput{}, withName(name, err)
	}
	return nil, spawnOutput{Name: name}, nil
}

// withName keeps the name of an agent that was created but is not ready in the
// error the caller sees, since it is how the agent is found afterwards.
func withName(name string, err error) error {
	if name == "" {
		return err
	}
	return fmt.Errorf("agent %q: %w", name, err)
}

type killInput struct {
	Name string `json:"name" jsonschema:"the temporary agent to kill"`
}

func (s *Server) killAgent(ctx context.Context, req *mcp.CallToolRequest, in killInput) (*mcp.CallToolResult, KillInfo, error) {
	if in.Name == "" {
		return nil, KillInfo{}, errors.New("a name is required")
	}
	res, err := s.ops.Kill(ctx, subject(req), in.Name)
	return nil, res, err
}

type listHarvestsOutput struct {
	Harvests []HarvestInfo `json:"harvests"`
}

func (s *Server) listHarvests(_ context.Context, req *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, listHarvestsOutput, error) {
	hs, err := s.ops.Harvests(subject(req))
	if hs == nil {
		hs = []HarvestInfo{}
	}
	return nil, listHarvestsOutput{Harvests: hs}, err
}

type readHarvestInput struct {
	ID   string `json:"id" jsonschema:"the harvest id (see list_harvests)"`
	Path string `json:"path,omitempty" jsonschema:"a file path inside the harvest; leave out to list the files"`
}

type readHarvestOutput struct {
	Harvest  *HarvestInfo `json:"harvest,omitempty" jsonschema:"the harvest and its files, when no path was given"`
	Path     string       `json:"path,omitempty" jsonschema:"the file read"`
	Encoding string       `json:"encoding,omitempty" jsonschema:"utf8 or base64"`
	Content  string       `json:"content,omitempty" jsonschema:"the file's content"`
}

func (s *Server) readHarvest(_ context.Context, req *mcp.CallToolRequest, in readHarvestInput) (*mcp.CallToolResult, readHarvestOutput, error) {
	who := subject(req)
	if in.ID == "" {
		return nil, readHarvestOutput{}, errors.New("an id is required")
	}
	if in.Path == "" {
		h, err := s.ops.Harvest(who, in.ID)
		if err != nil {
			return nil, readHarvestOutput{}, err
		}
		return nil, readHarvestOutput{Harvest: &h}, nil
	}
	b, err := s.ops.HarvestFile(who, in.ID, in.Path)
	if err != nil {
		return nil, readHarvestOutput{}, err
	}
	if len(b) > maxFileBytes {
		return nil, readHarvestOutput{}, fmt.Errorf("%s is %d bytes, more than the %d one call returns", in.Path, len(b), maxFileBytes)
	}
	out := readHarvestOutput{Path: in.Path, Encoding: "utf8", Content: string(b)}
	if !utf8.Valid(b) {
		out.Encoding, out.Content = "base64", base64.StdEncoding.EncodeToString(b)
	}
	return nil, out, nil
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

type sendInput struct {
	Name string `json:"name,omitempty" jsonschema:"the agent to send to; give this or tag"`
	Tag  string `json:"tag,omitempty" jsonschema:"send to every agent carrying this tag; give this or name"`
	Text string `json:"text" jsonschema:"the message"`
}

type sendOutput struct {
	Delivered []Delivered `json:"delivered"`
}

func (s *Server) sendMessage(ctx context.Context, req *mcp.CallToolRequest, in sendInput) (*mcp.CallToolResult, sendOutput, error) {
	if (in.Name == "") == (in.Tag == "") {
		return nil, sendOutput{}, errors.New("give exactly one of name and tag")
	}
	if in.Text == "" {
		return nil, sendOutput{}, errors.New("a message needs text")
	}
	target, byTag := in.Name, false
	if in.Tag != "" {
		target, byTag = in.Tag, true
	}
	ds, err := s.ops.Send(ctx, subject(req), byTag, target, in.Text)
	if ds == nil {
		ds = []Delivered{}
	}
	return nil, sendOutput{Delivered: ds}, err
}
