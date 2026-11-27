package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHowAServerIsReachedAndGivenItsCredentialIsChecked(t *testing.T) {
	tools := map[string]UpstreamTool{"q": {}}
	ok := []MCPUpstream{
		{URL: "https://mcp.example"},
		{URL: "https://mcp.example", CredentialEnv: "TOKEN"},
		{URL: "https://mcp.example", Secret: "key"},
		{URL: "https://mcp.example", Secret: "key", Header: "X-Api-Key"},
		{Command: []string{"server"}},
		{Command: []string{"server", "--flag"}, Secret: "key", SecretEnv: "SERVER_KEY"},
	}
	for _, u := range ok {
		u.Tools = tools
		assert.NoError(t, checkUpstreamWay(u), "%+v", u)
	}
	bad := map[string]MCPUpstream{
		"give url or command, not both":           {URL: "https://mcp.example", Command: []string{"x"}},
		"give url or command":                     {},
		"give credential_env or secret, not both": {URL: "https://mcp.example", CredentialEnv: "T", Secret: "key"},
		"is not a secret's name":                  {URL: "https://mcp.example", Secret: "Bad Name"},
		"url must be an http or https URL":        {URL: "ftp://mcp.example"},
		"secret_env is for a local server":        {URL: "https://mcp.example", Secret: "key", SecretEnv: "K"},
		"header must be a header name":            {URL: "https://mcp.example", Secret: "key", Header: "Bad Header"},
		"is given only with secret":               {URL: "https://mcp.example", Header: "X-Api-Key"},
		"command must name a program":             {Command: []string{""}},
		"credential_env is for an HTTP server":    {Command: []string{"x"}, CredentialEnv: "T"},
		"header is for an HTTP server":            {Command: []string{"x"}, Secret: "key", SecretEnv: "K", Header: "X-Api-Key"},
		"needs secret_env":                        {Command: []string{"x"}, Secret: "key"},
		"secret_env needs secret":                 {Command: []string{"x"}, SecretEnv: "K"},
		"secret_env must name an environment":     {Command: []string{"x"}, Secret: "key", SecretEnv: "bad-name"},
		"is a variable pao sets itself":           {Command: []string{"x"}, Secret: "key", SecretEnv: "PATH"},
	}
	for want, u := range bad {
		u.Tools = tools
		err := checkUpstreamWay(u)
		require.Error(t, err, want)
		assert.Contains(t, err.Error(), want)
	}
}

func TestAServersSecretExistsIsNotInjectedAndIsOneServersAlone(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	require.NoError(t, os.WriteFile(key, []byte("v"), 0o600))
	base := "node: n1\nprofiles_dir: profiles\nstate_dir: state\ntoken_env: PAO_TOKEN\n"
	secrets := "secrets:\n  - name: key\n    file: " + key + "\n  - name: inj\n    file: " + key + "\n    inject: {hosts: [api.acme.example], env: INJ}\n"
	upstream := func(name, secret string) string {
		return "  - name: " + name + "\n    url: https://" + name + ".example\n    secret: " + secret + "\n    tools: {q: {}}\n"
	}
	load := func(ups string) error {
		path := filepath.Join(t.TempDir(), "pao.yaml")
		write(t, path, base+secrets+"mcp_upstreams:\n"+ups)
		require.NoError(t, os.MkdirAll(filepath.Join(filepath.Dir(path), "profiles"), 0o755))
		_, err := Load(path)
		return err
	}
	require.NoError(t, load(upstream("one", "key")))
	for want, ups := range map[string]string{
		`no secret named "missing" is configured`:                upstream("one", "missing"),
		`is injected through the egress proxy`:                   upstream("one", "inj"),
		`secret "key" is already the credential of server "one"`: upstream("one", "key") + upstream("two", "key"),
	} {
		err := load(ups)
		require.Error(t, err, want)
		assert.Contains(t, err.Error(), want)
	}

	o := Orchestrator{MCPUpstreams: []MCPUpstream{{Name: "one", Secret: "key"}, {Name: "two"}}}
	assert.True(t, o.BoundToUpstream("key"))
	assert.False(t, o.BoundToUpstream("other"))
	assert.False(t, o.BoundToUpstream(""), "a server with no secret binds nothing")
}
