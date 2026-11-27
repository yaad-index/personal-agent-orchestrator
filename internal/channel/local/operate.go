package local

import (
	"context"
	"net/http"
	"time"
)

// QuestionJSON is an approval question waiting for the operator's answer
// (ADR-0004). It is answered at POST /v1/decisions.
type QuestionJSON struct {
	ID    string    `json:"id"`
	Agent string    `json:"agent"`
	Tool  string    `json:"tool"`
	Text  string    `json:"text"`
	Asked time.Time `json:"asked"`
}

// RecipeJSON is a recipe a temporary agent can be spawned from (ADR-0006).
type RecipeJSON struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
}

// WithQuestions serves the approval questions waiting on this node at
// GET /v1/questions, oldest first.
func WithQuestions(list func() []QuestionJSON) Option { return func(s *Server) { s.questions = list } }

// WithRecipes serves the recipes a temporary agent can be spawned from at
// GET /v1/recipes.
func WithRecipes(list func() ([]RecipeJSON, error)) Option {
	return func(s *Server) { s.recipes = list }
}

func (s *Server) registerOperate(mux *http.ServeMux) {
	if s.questions != nil {
		mux.HandleFunc("GET /v1/questions", s.auth(func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, s.questions())
		}))
	}
	if s.recipes != nil {
		mux.HandleFunc("GET /v1/recipes", s.auth(func(w http.ResponseWriter, _ *http.Request) {
			list, err := s.recipes()
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, list)
		}))
	}
}

// Questions lists the approval questions waiting on the node.
func (c *Client) Questions(ctx context.Context) ([]QuestionJSON, error) {
	var out []QuestionJSON
	err := c.do(ctx, http.MethodGet, "/v1/questions", nil, http.StatusOK, &out)
	return out, err
}

// Recipes lists the recipes a temporary agent can be spawned from.
func (c *Client) Recipes(ctx context.Context) ([]RecipeJSON, error) {
	var out []RecipeJSON
	err := c.do(ctx, http.MethodGet, "/v1/recipes", nil, http.StatusOK, &out)
	return out, err
}
