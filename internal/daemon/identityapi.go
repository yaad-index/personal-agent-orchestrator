package daemon

import (
	"context"
	"errors"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/identity"
)

// identityAPI is the operator's view of subjects and bindings
// (local.IdentityAPI), over this node's identity store.
type identityAPI struct{ s *identity.Store }

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

func (a identityAPI) Subjects(context.Context) ([]local.SubjectJSON, error) {
	subs, err := a.s.Subjects()
	out := make([]local.SubjectJSON, 0, len(subs))
	for _, s := range subs {
		out = append(out, local.SubjectJSON{Name: s.Name, Created: s.Created})
	}
	return out, identityErr(err)
}

func (a identityAPI) Bind(_ context.Context, b local.BindingJSON) error {
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
