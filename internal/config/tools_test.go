package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withDefaults(t *testing.T, defaults string, profiles map[string]string) string {
	t.Helper()
	path := setup(t, profiles)
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	write(t, path, string(b)+defaults)
	return path
}

func TestProfileDefaultsAndAProfilesOwnToolsAreRead(t *testing.T) {
	path := withDefaults(t, "profile_defaults:\n  tools:\n    disable: [AskUserQuestion, EnterPlanMode, ExitPlanMode]\n", map[string]string{
		"a.yaml": "name: a\nbackend: claude-code\n" + startup,
		"b.yaml": "name: b\nbackend: claude-code\n" + startup + "tools:\n  disable: [Bash]\n",
		"c.yaml": "name: c\nbackend: claude-code\n" + startup + "tools:\n  disable: []\n",
	})
	cfg, err := Load(path)
	require.NoError(t, err)
	require.NotNil(t, cfg.Orchestrator.ProfileDefaults.Tools.Disable)
	assert.Equal(t, []string{"AskUserQuestion", "EnterPlanMode", "ExitPlanMode"}, *cfg.Orchestrator.ProfileDefaults.Tools.Disable)

	a, b, c := cfg.Profiles[0], cfg.Profiles[1], cfg.Profiles[2]
	assert.Nil(t, a.Tools.Disable, "a profile that names nothing is left unset, so the default applies")
	assert.Equal(t, []string{"Bash"}, b.DisabledTools())
	require.NotNil(t, c.Tools.Disable, "an empty list is set: it turns the default off")
	assert.Empty(t, *c.Tools.Disable)

	d := cfg.Orchestrator.ProfileDefaults.Tools
	assert.Equal(t, []string{"AskUserQuestion", "EnterPlanMode", "ExitPlanMode"}, a.WithToolDefaults(d).DisabledTools())
	assert.Equal(t, []string{"Bash"}, b.WithToolDefaults(d).DisabledTools(), "a profile's own list replaces the default")
	assert.Empty(t, c.WithToolDefaults(d).DisabledTools())
}

func TestNoDefaultsIsNoTool(t *testing.T) {
	cfg, err := Load(setup(t, map[string]string{"a.yaml": "name: a\nbackend: claude-code\n" + startup}))
	require.NoError(t, err)
	assert.Empty(t, cfg.Profiles[0].WithToolDefaults(cfg.Orchestrator.ProfileDefaults.Tools).DisabledTools(),
		"without profile_defaults, pao turns nothing off on its own: it is configuration, not code")
}

func TestToolNamesAreChecked(t *testing.T) {
	for name, list := range map[string]string{
		"empty":   `[""]`,
		"padded":  `[" Bash"]`,
		"control": `["Bash\u0007"]`,
		"twice":   `[Bash, Bash]`,
	} {
		path := withDefaults(t, "profile_defaults:\n  tools:\n    disable: "+list+"\n", map[string]string{"a.yaml": "name: a\nbackend: claude-code\n" + startup})
		_, err := Load(path)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "profile_defaults.tools", name)

		path = setup(t, map[string]string{"a.yaml": "name: a\nbackend: claude-code\n" + startup + "tools:\n  disable: " + list + "\n"})
		_, err = Load(path)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "tools: disable", name)
	}
	path := setup(t, map[string]string{"a.yaml": "name: a\nbackend: claude-code\n" + startup + "tools:\n  enable: [Bash]\n"})
	_, err := Load(path)
	assert.Error(t, err, "an unknown key under tools is refused, not silently dropped")
}

func TestADefaultABackendCannotKeepIsRefusedAtLoad(t *testing.T) {
	other := "name: o\nbackend: other\ninherit: [instructions, mcp, skills, settings, memory]\n" + startup
	_, err := Load(setup(t, map[string]string{"o.yaml": other}))
	require.NoError(t, err, "a backend with no adapter runs a profile that inherits everything and turns nothing off")

	path := withDefaults(t, "profile_defaults:\n  tools:\n    disable: [AskUserQuestion]\n", map[string]string{"o.yaml": other})
	_, err = Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "with profile_defaults")
	assert.Contains(t, err.Error(), "cannot turn its tools off")

	path = withDefaults(t, "profile_defaults:\n  tools:\n    disable: [AskUserQuestion]\n", map[string]string{"o.yaml": other + "tools:\n  disable: []\n"})
	_, err = Load(path)
	assert.NoError(t, err, "its own empty list asks nothing of it")
}
