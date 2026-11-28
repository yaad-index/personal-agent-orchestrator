package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/adapter"
	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/local"
	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
)

// channelConfigAPI is the operator's view and change of the configuration's
// channels (local.ChannelConfigAPI). It reads the configuration from disk each
// time, since that is what pao starts with next, and changes only the file pao
// manages (ADR-0055); pao.yaml is never written.
type channelConfigAPI struct {
	// path is the orchestrator config pao was started with.
	path string
	// running are the channels the node runs, as it started with them.
	running []config.Channel
	// startEnv looks a variable up in the environment pao was started with,
	// which a restart from the UI starts with again.
	startEnv func(string) (string, bool)
	adapters adapter.Table
	// mu keeps one change at a time: each reads the file, changes it and
	// writes it whole.
	mu sync.Mutex
	// codes are the codes sent to targets to identify them.
	codes *identifyCodes
	// providers are the providers the node runs, by name, set once they are
	// built and before the API is served.
	providers map[string]adapter.Adapter
	// senders lists the sender ids bound to subject on provider.
	senders func(provider, subject string) ([]string, error)
}

func (a *channelConfigAPI) List(context.Context) (local.ChannelConfigListJSON, error) {
	cfg, err := config.Load(a.path)
	if err != nil {
		return local.ChannelConfigListJSON{}, err
	}
	o := cfg.Orchestrator
	out := local.ChannelConfigListJSON{
		ManagedFile: o.ManagedPath(),
		Channels:    []local.ChannelConfigEntryJSON{},
		Providers:   []local.ProviderJSON{},
	}
	carries := map[string][]string{}
	for _, c := range o.Channels {
		carries[c.Provider] = append(carries[c.Provider], c.Name)
		i := slices.IndexFunc(a.running, func(r config.Channel) bool { return r.Name == c.Name })
		applied := i >= 0 && reflect.DeepEqual(a.running[i], c)
		out.RestartNeeded = out.RestartNeeded || !applied
		out.Channels = append(out.Channels, local.ChannelConfigEntryJSON{
			ChannelConfigJSON: channelToJSON(c),
			Managed:           o.ManagedChannel(c.Name),
			Applied:           applied,
		})
	}
	for _, r := range a.running {
		if !slices.ContainsFunc(o.Channels, func(c config.Channel) bool { return c.Name == r.Name }) {
			out.RestartNeeded = true // removed from disk, still running
		}
	}
	for _, p := range o.Providers {
		out.Providers = append(out.Providers, local.ProviderJSON{Name: p.Name, Kind: p.Kind, Credential: a.credentialStatus(o, p), Channels: carries[p.Name]})
	}
	return out, nil
}

// credentialStatus says whether p's credential reference resolves where pao
// would read it at its next start, never what it resolves to (ADR-0057 point
// 4). A variable is looked for in the environment pao was started with: a
// restart from the UI starts with that one again, so a variable set anywhere
// else is not seen until the service itself is started again.
func (a *channelConfigAPI) credentialStatus(o config.Orchestrator, p config.Provider) local.CredentialStatusJSON {
	inEnv := func(name string) local.CredentialStatusJSON {
		if v, ok := a.startEnv(name); ok && v != "" {
			return local.CredentialStatusJSON{Resolves: true}
		}
		return local.CredentialStatusJSON{Problem: fmt.Sprintf("variable %s is not set in the environment pao was started with; set it where pao's service is configured, then start the service again", name)}
	}
	if p.CredentialEnv != "" {
		return inEnv(p.CredentialEnv)
	}
	s, ok := o.Secret(p.CredentialSecret)
	switch {
	case !ok:
		return local.CredentialStatusJSON{Problem: fmt.Sprintf("no secret named %q is configured", p.CredentialSecret)}
	case s.Env != "":
		return inEnv(s.Env)
	}
	b, err := os.ReadFile(s.File)
	switch {
	case err != nil:
		return local.CredentialStatusJSON{Problem: fmt.Sprintf("secret %q: file %s cannot be read", p.CredentialSecret, s.File)}
	case strings.TrimSpace(string(b)) == "":
		return local.CredentialStatusJSON{Problem: fmt.Sprintf("secret %q: file %s is empty", p.CredentialSecret, s.File)}
	}
	return local.CredentialStatusJSON{Resolves: true}
}

// Put defines or redefines a channel in the managed file. A new channel, or one
// put on another target, owner or provider, is written only with the code pao
// sent to that target (Identify): the operator shows they control it before it
// becomes a channel (ADR-0058 point 2).
func (a *channelConfigAPI) Put(_ context.Context, in local.ChannelConfigJSON) error {
	var c config.Channel
	var verify bool
	return a.change(func(o config.Orchestrator, m *config.Managed) error {
		var err error
		c, verify, err = a.put(o, m, in)
		return err
	}, func() error {
		if !verify {
			return nil
		}
		return a.codes.redeem(c.Name, whereOf(c), in.Code)
	})
}

// Identify sends a code to the target of the channel in would define, once the
// definition would be written: to the owner's private conversation with the
// provider's bot, or to the group or topic. The code is entered with the
// definition, through Put.
func (a *channelConfigAPI) Identify(ctx context.Context, in local.ChannelConfigJSON) (local.IdentifyJSON, error) {
	c, err := a.check(in)
	if err != nil {
		return local.IdentifyJSON{}, err
	}
	p, ok := a.providers[c.Provider]
	if !ok {
		return local.IdentifyJSON{}, translated{local.ErrConflict, fmt.Errorf("provider %q is not running, as pao was started without its credential or before it was defined; start pao again with it", c.Provider)}
	}
	ann, ok := p.(adapter.Announcer)
	if !ok {
		return local.IdentifyJSON{}, translated{local.ErrConflict, fmt.Errorf("provider %q cannot send to a conversation that is not a channel yet", c.Provider)}
	}
	to := []string{c.Target}
	if c.Target == config.TargetPrivate {
		if to, err = a.senders(c.Provider, c.Owner); err != nil {
			return local.IdentifyJSON{}, err
		}
		if len(to) == 0 {
			return local.IdentifyJSON{}, translated{local.ErrConflict, fmt.Errorf("%s is not registered on provider %q, so pao cannot write to them privately there: invite them, and have them send /register with the invitation to the bot", c.Owner, c.Provider)}
		}
	}
	code, exp, err := a.codes.issue(c.Name, whereOf(c))
	if err != nil {
		return local.IdentifyJSON{}, err
	}
	sent := 0
	var errs []error
	for _, conv := range to {
		if err := ann.Announce(ctx, conv, identifyText(c.Name, code, identifyTTL)); err != nil {
			errs = append(errs, err)
			continue
		}
		sent++
	}
	if sent == 0 {
		a.codes.drop(c.Name)
		return local.IdentifyJSON{}, translated{local.ErrConflict, fmt.Errorf("the code could not be sent: %w", errors.Join(errs...))}
	}
	return local.IdentifyJSON{Expires: exp, Sent: sent}, nil
}

// check reads the configuration and reports the channel in would define,
// refusing what Put would refuse, without writing anything.
func (a *channelConfigAPI) check(in local.ChannelConfigJSON) (config.Channel, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	o, m, err := a.managed()
	if err != nil {
		return config.Channel{}, err
	}
	c, _, err := a.put(o, &m, in)
	if err != nil {
		return config.Channel{}, err
	}
	if err := config.CheckManaged(a.path, m, func(cfg *config.Config) error { return a.startable(cfg.Orchestrator, false) }); err != nil {
		var t translated
		if errors.As(err, &t) {
			return config.Channel{}, err
		}
		return config.Channel{}, translated{local.ErrBadRequest, err}
	}
	return c, nil
}

// put changes m to hold the channel in defines and returns it, and whether it
// needs its target identified: it is new, or on another target, owner or
// provider than before.
func (a *channelConfigAPI) put(o config.Orchestrator, m *config.Managed, in local.ChannelConfigJSON) (config.Channel, bool, error) {
	c := channelFromJSON(in)
	if i := slices.IndexFunc(o.Channels, func(x config.Channel) bool { return x.Name == c.Name }); i >= 0 && !o.ManagedChannel(c.Name) {
		return config.Channel{}, false, translated{local.ErrConflict, fmt.Errorf("channel %q is defined in %s; change it there", c.Name, a.path)}
	}
	if c.ID == 0 {
		// A new channel is given its id: the one a channel of this name was
		// served under, or one never used (ADR-0021 point 2).
		l, err := ledgerOf(o)
		if err != nil {
			return config.Channel{}, false, err
		}
		if id, ok := l.IDOf(c.Name); ok {
			c.ID = id
		} else {
			c.ID = l.MaxID()
			for _, x := range o.Channels {
				c.ID = max(c.ID, x.ID)
			}
			c.ID++
		}
	}
	i := slices.IndexFunc(m.Channels, func(x config.Channel) bool { return x.Name == c.Name })
	if i < 0 {
		m.Channels = append(m.Channels, c)
		return c, true, nil
	}
	// Topics the form does not show are kept: a group named again by the
	// same platform id keeps the topics it had.
	for gi, g := range c.Groups {
		if g.Topics != nil {
			continue
		}
		if old := slices.IndexFunc(m.Channels[i].Groups, func(x config.Group) bool { return x.ID == g.ID }); old >= 0 {
			c.Groups[gi].Topics = m.Channels[i].Groups[old].Topics
		}
	}
	moved := whereOf(m.Channels[i]) != whereOf(c)
	m.Channels[i] = c
	return c, moved, nil
}

func (a *channelConfigAPI) Delete(_ context.Context, name string) error {
	return a.change(func(o config.Orchestrator, m *config.Managed) error {
		i := slices.IndexFunc(m.Channels, func(x config.Channel) bool { return x.Name == name })
		if i < 0 {
			if slices.ContainsFunc(o.Channels, func(x config.Channel) bool { return x.Name == name }) {
				return translated{local.ErrConflict, fmt.Errorf("channel %q is defined in %s; change it there", name, a.path)}
			}
			return translated{local.ErrNotFound, fmt.Errorf("no channel %q", name)}
		}
		m.Channels = slices.Delete(m.Channels, i, i+1)
		return nil
	})
}

// managed reads the configuration and the channels of the file pao manages.
func (a *channelConfigAPI) managed() (config.Orchestrator, config.Managed, error) {
	cfg, err := config.Load(a.path)
	if err != nil {
		return config.Orchestrator{}, config.Managed{}, err
	}
	o := cfg.Orchestrator
	if o.ManagedPath() == "" {
		return config.Orchestrator{}, config.Managed{}, translated{local.ErrConflict, config.ErrNotManaged}
	}
	var m config.Managed
	for _, c := range o.Channels {
		if o.ManagedChannel(c.Name) {
			m.Channels = append(m.Channels, c)
		}
	}
	return o, m, nil
}

// change reads the managed file's channels, lets edit change them, and writes
// the file whole, only if the configuration then loads, every channel's adapter
// accepts it (ADR-0055 point 4), and then each of last passes.
func (a *channelConfigAPI) change(edit func(config.Orchestrator, *config.Managed) error, last ...func() error) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	o, m, err := a.managed()
	if err != nil {
		return err
	}
	if err := edit(o, &m); err != nil {
		return err
	}
	checks := []func(*config.Config) error{func(cfg *config.Config) error { return a.startable(cfg.Orchestrator, false) }}
	for _, f := range last {
		checks = append(checks, func(*config.Config) error { return f() })
	}
	if err := config.WriteManaged(a.path, m, checks...); err != nil {
		var t translated
		if errors.As(err, &t) {
			return err
		}
		// what would not load is the request's to fix
		return translated{local.ErrBadRequest, err}
	}
	return nil
}

func channelToJSON(c config.Channel) local.ChannelConfigJSON {
	out := local.ChannelConfigJSON{ID: c.ID, Name: c.Name, Provider: c.Provider, Owner: c.Owner, Target: c.Target}
	for _, g := range c.Groups {
		gj := local.GroupConfigJSON{ID: g.ID, Name: g.Name, Members: g.Members}
		for _, t := range g.Topics {
			gj.Topics = append(gj.Topics, local.TopicConfigJSON{ID: t.ID, Name: t.Name})
		}
		out.Groups = append(out.Groups, gj)
	}
	return out
}

func channelFromJSON(in local.ChannelConfigJSON) config.Channel {
	c := config.Channel{ID: in.ID, Name: in.Name, Provider: in.Provider, Owner: in.Owner, Target: in.Target}
	for _, g := range in.Groups {
		cg := config.Group{ID: g.ID, Name: g.Name, Members: g.Members}
		if cg.Members == "" {
			cg.Members = config.MembersOperators
		}
		for _, t := range g.Topics {
			cg.Topics = append(cg.Topics, config.Topic{ID: t.ID, Name: t.Name})
		}
		c.Groups = append(c.Groups, cg)
	}
	return c
}

// ledgerOf opens the node's record of the channel ids it has served.
func ledgerOf(o config.Orchestrator) (*adapter.Ledger, error) {
	return adapter.OpenLedger(filepath.Join(o.StateDir, "channels.json"))
}

// startable checks what starting the node would check of its channels: each
// provider's adapter accepts its kind and options, and no channel id is reused
// or name given another id (ADR-0021 point 2). With credentials, each provider
// that carries a channel must also have its credential there to read, as
// starting needs: a write may put a channel on one not provisioned yet, but a
// restart with it would stop the node.
func (a *channelConfigAPI) startable(o config.Orchestrator, credentials bool) error {
	if err := a.adapters.CheckConfig(a.path, o); err != nil {
		return err
	}
	l, err := ledgerOf(o)
	if err != nil {
		return err
	}
	specs := make([]adapter.Spec, 0, len(o.Channels))
	for _, c := range o.Channels {
		specs = append(specs, adapter.Spec{ID: c.ID, Name: c.Name})
	}
	if err := l.Check(specs); err != nil {
		return err
	}
	if credentials {
		// A provider that carries no channel is started only if its
		// credential is there, so it does not need one yet.
		for _, p := range o.Providers {
			if !slices.ContainsFunc(o.Channels, func(c config.Channel) bool { return c.Provider == p.Name }) {
				continue
			}
			if st := a.credentialStatus(o, p); !st.Resolves {
				return fmt.Errorf("provider %q: %s", p.Name, st.Problem)
			}
		}
	}
	return nil
}

// RestartCheck is what a restart from the UI must pass first: the
// configuration on disk loads, and its channels would start. Otherwise pao
// would stop and not come back, so the restart is refused and pao keeps
// running as it is.
func (a *channelConfigAPI) RestartCheck() error {
	cfg, err := config.Load(a.path)
	if err != nil {
		return fmt.Errorf("pao would not start: %w", err)
	}
	if err := a.startable(cfg.Orchestrator, true); err != nil {
		return fmt.Errorf("pao would not start: %w", err)
	}
	if v, ok := a.startEnv(cfg.Orchestrator.TokenEnv); !ok || v == "" {
		return fmt.Errorf("pao would not start: variable %s (token_env) is not set in the environment pao was started with", cfg.Orchestrator.TokenEnv)
	}
	return nil
}
