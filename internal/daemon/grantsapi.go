package daemon

import (
	"context"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/grants"
)

// grantsFile is the stored document of the access matrix's grants, in the state
// directory.
const grantsFile = "grants.json"

// grantsAPI is the operator's administration of the access matrix
// (local.GrantsAPI). A grant may name an agent that does not exist yet, such as a
// temporary agent still to be spawned.
type grantsAPI struct {
	store *grants.Store
	// changed, when set, is told that the grants changed, so what is offered to
	// connected subjects follows them. It runs in the background: a grant takes
	// effect when it is stored, not when everyone has heard of it.
	changed func(context.Context)
}

// refreshTimeout bounds telling connected subjects about a grant change, which
// lists the upstream servers' tools.
const refreshTimeout = 30 * time.Second

func (a grantsAPI) Grants(_ context.Context, subject string) (local.GrantsJSON, error) {
	var only access.Subject
	if subject != "" {
		s, err := access.ParseSubject(subject)
		if err != nil {
			return local.GrantsJSON{}, translated{local.ErrBadRequest, err}
		}
		only = s
	}
	d := a.store.Document()
	out := local.GrantsJSON{Version: d.Version, Grants: make([]local.GrantJSON, 0, len(d.Grants))}
	for _, e := range d.Grants {
		if subject != "" && e.Subject != only.String() {
			continue
		}
		out.Grants = append(out.Grants, local.GrantJSON(e))
	}
	return out, nil
}

func (a grantsAPI) AddGrant(_ context.Context, g local.GrantJSON) (local.GrantChangeJSON, error) {
	return a.change(g, a.store.Add)
}

func (a grantsAPI) RemoveGrant(_ context.Context, g local.GrantJSON) (local.GrantChangeJSON, error) {
	return a.change(g, a.store.Remove)
}

func (a grantsAPI) change(g local.GrantJSON, f func(access.Grant) (bool, error)) (local.GrantChangeJSON, error) {
	grant, err := grants.Entry(g).Grant()
	if err != nil {
		return local.GrantChangeJSON{}, translated{local.ErrBadRequest, err}
	}
	changed, err := f(grant)
	if err == nil && changed && a.changed != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
			defer cancel()
			a.changed(ctx)
		}()
	}
	return local.GrantChangeJSON{Changed: changed}, err
}
