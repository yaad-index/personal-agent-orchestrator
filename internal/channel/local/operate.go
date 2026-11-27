package local

import (
	"context"
	"net/http"
)

// RecipeJSON is a recipe a temporary agent can be spawned from (ADR-0006).
type RecipeJSON struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
}

// WithRecipes serves the recipes a temporary agent can be spawned from at
// GET /v1/recipes.
func WithRecipes(list func() ([]RecipeJSON, error)) Option {
	return func(s *Server) { s.recipes = list }
}

func (s *Server) registerOperate(mux *http.ServeMux) {
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

// Recipes lists the recipes a temporary agent can be spawned from.
func (c *Client) Recipes(ctx context.Context) ([]RecipeJSON, error) {
	var out []RecipeJSON
	err := c.do(ctx, http.MethodGet, "/v1/recipes", nil, http.StatusOK, &out)
	return out, err
}
