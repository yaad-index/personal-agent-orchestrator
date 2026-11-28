package local

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

// ChannelConfigJSON is a channel's definition as the configuration holds it.
// Its credential is named, by a variable or a secret, never given: no request
// or answer here carries a credential's value (ADR-0057 point 1).
type ChannelConfigJSON struct {
	ID       int               `json:"id"`
	Name     string            `json:"name"`
	Provider string            `json:"provider"`
	Owner    string            `json:"owner"`
	Target   string            `json:"target"`
	Groups   []GroupConfigJSON `json:"groups,omitempty"`
	// Code is the code pao sent to the target (Identify), given when a channel
	// is defined or put on another target, owner or provider. It is never in
	// an answer.
	Code string `json:"code,omitempty"`
}

// IdentifyJSON answers a request to identify a channel's target: the code is
// sent, to that many conversations, and can be entered until Expires.
type IdentifyJSON struct {
	Expires time.Time `json:"expires"`
	Sent    int       `json:"sent"`
}

// ProviderJSON is a provider as the UI shows it (ADR-0058 point 1): defined in
// the configuration only, so the UI lists it and changes nothing of it. Its
// credential is shown only as resolving or not (ADR-0057 point 4).
type ProviderJSON struct {
	Name       string               `json:"name"`
	Kind       string               `json:"kind"`
	Credential CredentialStatusJSON `json:"credential"`
	// Channels are the channels it carries, by name, none if it carries none.
	Channels []string `json:"channels,omitempty"`
}

// GroupConfigJSON is a group a channel serves, as the configuration holds it.
type GroupConfigJSON struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Members string            `json:"members"`
	Topics  []TopicConfigJSON `json:"topics,omitempty"`
}

// TopicConfigJSON is a named topic of a group.
type TopicConfigJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// CredentialStatusJSON says whether a channel's credential reference resolves,
// and if not, why, naming where it looked; never what it found (ADR-0057
// point 4).
type CredentialStatusJSON struct {
	Resolves bool   `json:"resolves"`
	Problem  string `json:"problem,omitempty"`
}

// ChannelConfigEntryJSON is one channel of the configuration on disk.
type ChannelConfigEntryJSON struct {
	ChannelConfigJSON
	// Managed is true for a channel in the file pao manages, which the UI may
	// change, and false for one in pao.yaml, which it shows only (ADR-0055).
	Managed bool `json:"managed"`
	// Applied is true when the running node uses this definition as it is;
	// false until pao next starts.
	Applied bool `json:"applied"`
}

// ChannelConfigListJSON is the configuration's channels and what a form needs
// to define one.
type ChannelConfigListJSON struct {
	// ManagedFile is the file pao manages, or empty when the configuration
	// names none, in which case nothing here can be changed.
	ManagedFile string                   `json:"managed_file"`
	Channels    []ChannelConfigEntryJSON `json:"channels"`
	// RestartNeeded is true when what is on disk differs from what runs.
	RestartNeeded bool `json:"restart_needed"`
	// Providers are the providers the configuration defines, which a channel
	// is put on.
	Providers []ProviderJSON `json:"providers"`
}

// ChannelConfigAPI is the operator's view and change of the channels pao
// manages (ADR-0055, ADR-0057).
type ChannelConfigAPI interface {
	List(ctx context.Context) (ChannelConfigListJSON, error)
	// Put defines or redefines a channel in the managed file. A new channel,
	// or one put on another target, owner or provider, carries the code
	// Identify sent there.
	Put(ctx context.Context, c ChannelConfigJSON) error
	// Identify sends a one-time code to the target of the channel c would
	// define, refusing what Put would refuse.
	Identify(ctx context.Context, c ChannelConfigJSON) (IdentifyJSON, error)
	// Delete removes a channel from the managed file.
	Delete(ctx context.Context, name string) error
}

// WithChannelConfig serves the channel configuration routes.
func WithChannelConfig(a ChannelConfigAPI) Option { return func(s *Server) { s.channelConfig = a } }

func (s *Server) registerChannelConfig(mux *http.ServeMux) {
	if s.channelConfig == nil {
		return
	}
	mux.HandleFunc("GET /v1/config/channels", s.auth(func(w http.ResponseWriter, r *http.Request) {
		out, err := s.channelConfig.List(r.Context())
		if err != nil {
			writeError(w, tempStatus(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, out)
	}))
	mux.HandleFunc("PUT /v1/config/channels/{name}", s.auth(func(w http.ResponseWriter, r *http.Request) {
		var c ChannelConfigJSON
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&c); err != nil {
			writeError(w, http.StatusBadRequest, "the body must be a channel's definition")
			return
		}
		if c.Name != r.PathValue("name") {
			writeError(w, http.StatusBadRequest, "the channel's name is the one in the path")
			return
		}
		if err := s.channelConfig.Put(r.Context(), c); err != nil {
			writeError(w, tempStatus(err), err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("POST /v1/config/channels/{name}/identify", s.auth(func(w http.ResponseWriter, r *http.Request) {
		var c ChannelConfigJSON
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&c); err != nil {
			writeError(w, http.StatusBadRequest, "the body must be a channel's definition")
			return
		}
		if c.Name != r.PathValue("name") {
			writeError(w, http.StatusBadRequest, "the channel's name is the one in the path")
			return
		}
		out, err := s.channelConfig.Identify(r.Context(), c)
		if err != nil {
			writeError(w, tempStatus(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, out)
	}))
	mux.HandleFunc("DELETE /v1/config/channels/{name}", s.auth(func(w http.ResponseWriter, r *http.Request) {
		if err := s.channelConfig.Delete(r.Context(), r.PathValue("name")); err != nil {
			writeError(w, tempStatus(err), err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
}

// ChannelConfig lists the configuration's channels.
func (c *Client) ChannelConfig(ctx context.Context) (ChannelConfigListJSON, error) {
	var out ChannelConfigListJSON
	err := c.do(ctx, http.MethodGet, "/v1/config/channels", nil, http.StatusOK, &out)
	return out, err
}

// PutChannelConfig defines or redefines a channel in the managed file.
func (c *Client) PutChannelConfig(ctx context.Context, ch ChannelConfigJSON) error {
	return c.do(ctx, http.MethodPut, "/v1/config/channels/"+url.PathEscape(ch.Name), ch, http.StatusNoContent, nil)
}

// IdentifyChannel asks pao to send a code to the target of the channel ch
// would define.
func (c *Client) IdentifyChannel(ctx context.Context, ch ChannelConfigJSON) (IdentifyJSON, error) {
	var out IdentifyJSON
	err := c.do(ctx, http.MethodPost, "/v1/config/channels/"+url.PathEscape(ch.Name)+"/identify", ch, http.StatusOK, &out)
	return out, err
}

// DeleteChannelConfig removes a channel from the managed file.
func (c *Client) DeleteChannelConfig(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/v1/config/channels/"+url.PathEscape(name), nil, http.StatusNoContent, nil)
}
