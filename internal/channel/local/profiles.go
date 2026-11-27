package local

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
)

// Profile statuses, as the node compares a profile's file with the agent it
// runs.
const (
	// ProfileLoaded is a profile the node runs as written.
	ProfileLoaded = "loaded"
	// ProfileRestart is a profile saved while its agent runs: the agent keeps
	// the profile it started with until it is restarted.
	ProfileRestart = "restart_to_apply"
	// ProfileNotApplied is a file that differs from what the node runs, or
	// names an agent it does not run, because it was written outside the UI
	// after the node started. Saving it applies it.
	ProfileNotApplied = "not_applied"
	// ProfileInvalid is a file the node would not load.
	ProfileInvalid = "invalid"
)

// ProfileJSON is one profile file in the node's profiles directory.
type ProfileJSON struct {
	// File is the file's name, which addresses the profile.
	File string `json:"file"`
	// Name is the agent the profile defines; empty for an invalid file.
	Name    string   `json:"name"`
	Backend string   `json:"backend,omitempty"`
	Tags    []string `json:"tags"`
	Status  string   `json:"status"`
	// Running says the agent is running.
	Running bool `json:"running"`
	// Error is why an invalid file would not load.
	Error string `json:"error,omitempty"`
}

// ProfileFileJSON is a profile with its file's text.
type ProfileFileJSON struct {
	ProfileJSON
	Text string `json:"text"`
}

// ProfileDraftJSON is what a new profile's starting text is made from.
type ProfileDraftJSON struct {
	Name    string   `json:"name"`
	Workdir string   `json:"workdir"`
	Model   string   `json:"model"`
	Tags    []string `json:"tags"`
}

// ProfileTextJSON is the text a profile is saved as.
type ProfileTextJSON struct {
	Text string `json:"text"`
}

// Profile errors a ProfilesAPI returns, each its own status.
var (
	// ErrInvalid is a profile the node would not load.
	ErrInvalid = errors.New("invalid profile")
	// ErrExists is a profile that would take a file or an agent's name already taken.
	ErrExists = errors.New("already exists")
)

// ProfilesAPI reads and writes the node's agent profiles (ADR-0047 point 2).
type ProfilesAPI interface {
	List() ([]ProfileJSON, error)
	Get(file string) (ProfileFileJSON, error)
	// Create writes a new profile and applies it.
	Create(text string) (ProfileJSON, error)
	// Save replaces a profile's text and applies it.
	Save(file, text string) (ProfileJSON, error)
	// Draft is a new profile's starting text, for the operator to read and
	// change before creating it. Nothing is written.
	Draft(d ProfileDraftJSON) (string, error)
}

// WithProfiles serves the node's profiles at /v1/profiles.
func WithProfiles(p ProfilesAPI) Option { return func(s *Server) { s.profiles = p } }

func (s *Server) registerProfiles(mux *http.ServeMux) {
	if s.profiles == nil {
		return
	}
	mux.HandleFunc("GET /v1/profiles", s.auth(func(w http.ResponseWriter, _ *http.Request) {
		list, err := s.profiles.List()
		if err != nil {
			writeError(w, profileStatus(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, list)
	}))
	mux.HandleFunc("GET /v1/profiles/{file}", s.auth(func(w http.ResponseWriter, r *http.Request) {
		p, err := s.profiles.Get(r.PathValue("file"))
		if err != nil {
			writeError(w, profileStatus(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, p)
	}))
	mux.HandleFunc("POST /v1/profiles/draft", s.auth(func(w http.ResponseWriter, r *http.Request) {
		var d ProfileDraftJSON
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&d); err != nil {
			writeError(w, http.StatusBadRequest, "the body must be {\"name\": ..., \"workdir\": ..., \"model\": ..., \"tags\": [...]}")
			return
		}
		text, err := s.profiles.Draft(d)
		if err != nil {
			writeError(w, profileStatus(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, ProfileTextJSON{Text: text})
	}))
	mux.HandleFunc("POST /v1/profiles", s.auth(func(w http.ResponseWriter, r *http.Request) {
		var body ProfileTextJSON
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "the body must be {\"text\": ...}")
			return
		}
		p, err := s.profiles.Create(body.Text)
		if err != nil {
			writeError(w, profileStatus(err), err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, p)
	}))
	mux.HandleFunc("PUT /v1/profiles/{file}", s.auth(func(w http.ResponseWriter, r *http.Request) {
		var body ProfileTextJSON
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "the body must be {\"text\": ...}")
			return
		}
		p, err := s.profiles.Save(r.PathValue("file"), body.Text)
		if err != nil {
			writeError(w, profileStatus(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, p)
	}))
}

func profileStatus(err error) int {
	switch {
	case errors.Is(err, ErrInvalid):
		return http.StatusBadRequest
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrExists):
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

// Profiles lists the node's profiles.
func (c *Client) Profiles(ctx context.Context) ([]ProfileJSON, error) {
	var out []ProfileJSON
	err := c.do(ctx, http.MethodGet, "/v1/profiles", nil, http.StatusOK, &out)
	return out, err
}

// Profile reads one profile with its text.
func (c *Client) Profile(ctx context.Context, file string) (ProfileFileJSON, error) {
	var out ProfileFileJSON
	err := c.do(ctx, http.MethodGet, "/v1/profiles/"+url.PathEscape(file), nil, http.StatusOK, &out)
	return out, err
}

// CreateProfile writes a new profile.
func (c *Client) CreateProfile(ctx context.Context, text string) (ProfileJSON, error) {
	var out ProfileJSON
	err := c.do(ctx, http.MethodPost, "/v1/profiles", ProfileTextJSON{Text: text}, http.StatusCreated, &out)
	return out, err
}

// SaveProfile replaces a profile's text.
func (c *Client) SaveProfile(ctx context.Context, file, text string) (ProfileJSON, error) {
	var out ProfileJSON
	err := c.do(ctx, http.MethodPut, "/v1/profiles/"+url.PathEscape(file), ProfileTextJSON{Text: text}, http.StatusOK, &out)
	return out, err
}

// DraftProfile is a new profile's starting text.
func (c *Client) DraftProfile(ctx context.Context, d ProfileDraftJSON) (string, error) {
	var out ProfileTextJSON
	err := c.do(ctx, http.MethodPost, "/v1/profiles/draft", d, http.StatusOK, &out)
	return out.Text, err
}
