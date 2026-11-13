package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/recipe"
)

// RecipesCmd groups the commands on the recipes temporary agents spawn from.
type RecipesCmd struct {
	List RecipesListCmd `cmd:"" help:"List the recipes available to spawn from."`
	Pull RecipesPullCmd `cmd:"" help:"Pull a set of recipes from a git repository."`
}

// RecipesListCmd lists the recipes under the node's recipes directory.
type RecipesListCmd struct {
	Config string `arg:"" type:"existingfile" help:"Path to the orchestrator config file."`
}

// Run implements recipes list.
func (c RecipesListCmd) Run() error {
	cfg, err := config.Load(c.Config)
	if err != nil {
		return err
	}
	return listRecipes(os.Stdout, cfg.Orchestrator.RecipesDir)
}

func listRecipes(w io.Writer, dir string) error {
	set, err := recipe.LoadDir(dir)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, n := range set.Names() {
		r, _ := set.Get(n)
		if _, err := fmt.Fprintf(tw, "%s\t%s\n", n, r.Description); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// RecipesPullCmd installs a set of recipes from a git repository. The
// recipes' startup commands run in a terminal when an agent spawns from them,
// so pull only from a repository you trust.
type RecipesPullCmd struct {
	Config     string `arg:"" type:"existingfile" help:"Path to the orchestrator config file."`
	URL        string `arg:"" help:"Git repository to pull."`
	Ref        string `help:"Branch or tag. Default: the repository's default branch."`
	Name       string `help:"Name of the set's directory. Default: derived from the URL."`
	Replace    bool   `help:"Replace an installed set of the same name."`
	AllowLocal bool   `name:"allow-local" help:"Allow a repository on this machine."`
}

// Run implements recipes pull.
func (c RecipesPullCmd) Run() error { return c.run(os.Stdout) }

func (c RecipesPullCmd) run(w io.Writer) error {
	cfg, err := config.Load(c.Config)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	names, err := recipe.Pull(ctx, recipe.PullOptions{
		Dir: cfg.Orchestrator.RecipesDir, URL: c.URL, Ref: c.Ref, Set: c.Name, Replace: c.Replace, AllowLocal: c.AllowLocal,
	})
	if errors.Is(err, recipe.ErrSetExists) {
		return fmt.Errorf("%w (use --replace to overwrite it)", err)
	}
	if err != nil {
		return err
	}
	for _, n := range names {
		if _, err := fmt.Fprintln(w, n); err != nil {
			return err
		}
	}
	return nil
}
