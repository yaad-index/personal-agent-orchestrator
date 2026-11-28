package daemon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/identity"
)

// identityAPI is the operator's view of subjects and bindings
// (local.IdentityAPI), over this node's identity store.
type identityAPI struct {
	s *identity.Store
	// providers are the providers the node was started with. A sender is
	// bound on its provider, so a binding under any other name would never be
	// read.
	providers []string
	// owners holds the owner of each channel the node runs, by name.
	owners map[string]string
	// logs returns the agents' log channels, by channel name; nil for none.
	logs func() map[string]string
}

func identityErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, identity.ErrNoSubject), errors.Is(err, identity.ErrNoBinding), errors.Is(err, identity.ErrNoInvitation):
		return translated{local.ErrNotFound, err}
	case errors.Is(err, identity.ErrSubjectExists), errors.Is(err, identity.ErrBound):
		return translated{local.ErrConflict, err}
	case errors.Is(err, identity.ErrInvalid):
		return translated{local.ErrBadRequest, err}
	}
	return err
}

func (a identityAPI) AddSubject(_ context.Context, name string) error {
	return identityErr(a.s.AddSubject(name))
}

func (a identityAPI) RemoveSubject(_ context.Context, name string) error {
	return identityErr(a.s.RemoveSubject(name))
}

func (a identityAPI) SetOperator(_ context.Context, name string, on bool) error {
	return identityErr(a.s.SetOperator(name, on))
}

// SetPermission sets a subject's permission channel: one of the channels the
// node runs that the subject owns, since pao asks there about the subject's
// agents (ADR-0058 point 6), or "" for the subject's private conversation.
func (a identityAPI) SetPermission(_ context.Context, name, channel string) error {
	if owner, ok := a.owners[channel]; channel != "" && (!ok || owner != name) {
		if !ok {
			return translated{local.ErrBadRequest, fmt.Errorf("no channel %q", channel)}
		}
		return translated{local.ErrBadRequest, fmt.Errorf("channel %q is %s's, not %s's: a permission channel is one of the user's own", channel, owner, name)}
	}
	if a.logs != nil {
		if of, ok := a.logs()[channel]; ok {
			return translated{local.ErrBadRequest, fmt.Errorf("channel %q is agent %q's log channel, which takes no input, so no question there could be answered", channel, of)}
		}
	}
	return identityErr(a.s.SetPermission(name, channel))
}

func (a identityAPI) Subjects(context.Context) ([]local.SubjectJSON, error) {
	subs, err := a.s.Subjects()
	out := make([]local.SubjectJSON, 0, len(subs))
	for _, s := range subs {
		out = append(out, local.SubjectJSON{Name: s.Name, Created: s.Created, Operator: s.Operator, Permission: s.Permission})
	}
	return out, identityErr(err)
}

func (a identityAPI) Bind(_ context.Context, b local.BindingJSON) error {
	if !slices.Contains(a.providers, b.Channel) {
		return translated{local.ErrBadRequest, fmt.Errorf("no provider %q: a sender is bound on the provider it writes through, not on a channel (have: %q)", b.Channel, a.providers)}
	}
	return identityErr(a.s.Bind(b.Channel, b.Sender, b.Subject))
}

func (a identityAPI) Revoke(_ context.Context, channel, sender string) error {
	return identityErr(a.s.Revoke(channel, sender))
}

func (a identityAPI) Bindings(_ context.Context, subject string) ([]local.BindingJSON, error) {
	bs, err := a.s.Bindings(subject)
	out := make([]local.BindingJSON, 0, len(bs))
	for _, b := range bs {
		out = append(out, local.BindingJSON{Channel: b.Channel, Sender: b.Sender, Subject: b.Subject})
	}
	return out, identityErr(err)
}

func (a identityAPI) Invite(_ context.Context, subject string, ttl time.Duration) (local.InvitationJSON, error) {
	secret, inv, err := a.s.Invite(subject, ttl)
	if err != nil {
		return local.InvitationJSON{}, identityErr(err)
	}
	return local.InvitationJSON{ID: inv.ID, Subject: inv.Subject, Expires: inv.Expires, Secret: secret}, nil
}

func (a identityAPI) Invitations(context.Context) ([]local.InvitationJSON, error) {
	invs, err := a.s.Invitations()
	out := make([]local.InvitationJSON, 0, len(invs))
	for _, i := range invs {
		out = append(out, local.InvitationJSON{ID: i.ID, Subject: i.Subject, Expires: i.Expires})
	}
	return out, identityErr(err)
}

func (a identityAPI) RevokeInvitation(_ context.Context, id string) error {
	return identityErr(a.s.RevokeInvitation(id))
}

// channelOwners maps each channel to its owner.
func channelOwners(chs []config.Channel) map[string]string {
	out := make(map[string]string, len(chs))
	for _, c := range chs {
		out[c.Name] = c.Owner
	}
	return out
}

// providerNames lists the configured providers by name.
func providerNames(ps []config.Provider) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Name)
	}
	return out
}
