package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/backend/claudecode"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/control"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/harvest"
	"github.com/yaad-index/personal-agent-orchestrator/internal/registry"
	"github.com/yaad-index/personal-agent-orchestrator/internal/temp"
	"github.com/yaad-index/personal-agent-orchestrator/internal/workdir"
)

// locators maps a transcript format to where its backend writes the
// transcripts of sessions started in a directory (ADR-0011). A backend joins
// pao here, next to its reader.
var locators = map[string]func(home, workdir string) string{
	claudecode.Format: claudecode.TranscriptDir,
}

// locate is the recipe.Locator for this node.
func locate(format, wd string) (string, bool) {
	f, ok := locators[format]
	if !ok {
		return "", false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	return f(home, wd), true
}

// checkProfile refuses a profile this node has no reader for.
func checkProfile(p core.Profile) error {
	if _, ok := readers[p.Transcript.Format]; !ok {
		return fmt.Errorf("unknown transcript format %q", p.Transcript.Format)
	}
	return nil
}

// tempAPI is the operator's view of the Manager and the harvest store, in the
// shape the local channel serves. The channel's bearer token identifies the
// operator, so every call is made as the operator.
type tempAPI struct {
	mgr   *temp.Manager
	store *harvest.Store
}

// translated carries a domain error under the status-bearing sentinel the
// channel maps to a code, keeping the domain error's own words.
type translated struct {
	kind error
	err  error
}

func (t translated) Error() string        { return t.err.Error() }
func (t translated) Is(target error) bool { return target == t.kind }
func (t translated) Unwrap() error        { return t.err }

func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, temp.ErrNotFound), errors.Is(err, temp.ErrNoRecipe), errors.Is(err, harvest.ErrNotFound):
		return translated{local.ErrNotFound, err}
	case errors.Is(err, temp.ErrBadRequest), errors.Is(err, workdir.ErrBadSource):
		return translated{local.ErrBadRequest, err}
	case errors.Is(err, registry.ErrDuplicate), errors.Is(err, workdir.ErrExists):
		return translated{local.ErrConflict, err}
	case errors.Is(err, temp.ErrDenied):
		return translated{local.ErrForbidden, err}
	}
	if _, notReady := control.AsReadinessError(err); notReady {
		return translated{local.ErrNotReady, err}
	}
	return err
}

func (a tempAPI) Spawn(ctx context.Context, recipe, name, channel string) (string, error) {
	p, err := a.mgr.Spawn(ctx, temp.SpawnRequest{Recipe: recipe, Name: name, Channel: channel, Requester: access.Operator})
	return p.Name, translate(err)
}

func (a tempAPI) Kill(ctx context.Context, name string) (local.KillJSON, error) {
	res, err := a.mgr.Kill(ctx, temp.KillRequest{Name: name, Requester: access.Operator})
	if err != nil {
		return local.KillJSON{}, translate(err)
	}
	return local.KillJSON{Harvest: res.Harvest, WorkdirKept: res.WorkdirKept}, nil
}

func (a tempAPI) Agents() []local.AgentJSON {
	var out []local.AgentJSON
	for _, s := range a.mgr.List() {
		out = append(out, local.AgentJSON{
			Name: s.Name, Recipe: s.Recipe, Channel: s.Channel, State: s.State.String(),
			Flag: string(s.Flag), Created: s.Created, LastActivity: s.LastActivity,
		})
	}
	return out
}

func (a tempAPI) Harvests() ([]harvest.Manifest, error) { return a.store.List(access.Operator) }

func (a tempAPI) Harvest(id string) (harvest.Manifest, error) {
	m, err := a.store.Get(access.Operator, id)
	return m, translate(err)
}

func (a tempAPI) HarvestFile(id, path string) ([]byte, error) {
	b, err := a.store.Read(access.Operator, id, path)
	return b, translate(err)
}

func (a tempAPI) DeleteHarvest(id string) error {
	return translate(a.store.Delete(access.Operator, id))
}
