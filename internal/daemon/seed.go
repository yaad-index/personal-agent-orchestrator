package daemon

import (
	"fmt"
	"log/slog"
	"os"
	"slices"

	"github.com/yaad-index/personal-agent-orchestrator/internal/access"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
	"github.com/yaad-index/personal-agent-orchestrator/internal/grants"
	"github.com/yaad-index/personal-agent-orchestrator/internal/recipe"
)

// operatorSeed is what the operator reached before the access matrix bounded it,
// as explicit grants: a tag scope for every tag a configured agent or recipe
// carries, and a cell for every configured agent with no tag. An agent spawned
// from a recipe with no tag gets a name chosen at spawn, so no grant can name it
// ahead of time; those recipes are returned so they can be reported.
func operatorSeed(profiles []core.Profile, recipes *recipe.Set) (seed []access.Grant, untagged []string) {
	var tags []string
	add := func(ts []string) {
		for _, t := range ts {
			if !slices.Contains(tags, t) {
				tags = append(tags, t)
			}
		}
	}
	for _, p := range profiles {
		if len(p.Tags) == 0 {
			seed = append(seed, access.Grant{Subject: access.Operator, Kind: access.ResourceAgent, Name: p.Name})
		}
		add(p.Tags)
	}
	for _, n := range recipes.Names() {
		r, _ := recipes.Get(n)
		if len(r.Profile.Tags) == 0 {
			untagged = append(untagged, n)
		}
		add(r.Profile.Tags)
	}
	slices.Sort(tags)
	for _, t := range tags {
		seed = append(seed, access.Grant{Subject: access.Operator, Kind: access.ResourceAgent, Tag: t})
	}
	return seed, untagged
}

// seedOperator writes the operator's seed into a node that has run before, the
// first time it starts with an access matrix, so the upgrade does not cut the
// operator off from its own agents. A node that has never run starts with no
// grants at all (ADR-0012 point 2), and a node that has grants already is left
// as it is. ranBefore says whether the node had kept state before this start.
func seedOperator(store *grants.Store, ranBefore bool, profiles []core.Profile, recipesDir string) error {
	if store.Found() || !ranBefore {
		return nil
	}
	recipes, err := recipe.LoadDir(recipesDir)
	if err != nil {
		return fmt.Errorf("seeding the operator's grants: %w", err)
	}
	seed, untagged := operatorSeed(profiles, recipes)
	if err := store.Seed(seed); err != nil {
		return fmt.Errorf("seeding the operator's grants: %w", err)
	}
	for _, g := range seed {
		slog.Info("seeded an operator grant from the configuration", "grant", grants.EntryOf(g))
	}
	for _, n := range untagged {
		slog.Warn("a recipe has no tag, so no seeded grant reaches the agents spawned from it; tag the recipe or grant the operator a name", "recipe", n)
	}
	return nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
