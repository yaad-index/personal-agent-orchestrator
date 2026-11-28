package config

import (
	"fmt"
	"path/filepath"
	"slices"
)

// BoundToProvider reports whether the secret named name is a provider's
// credential, which no task may name and no other provider or server may
// present (ADR-0057 point 3, the provider's by ADR-0058 point 1).
func (o Orchestrator) BoundToProvider(name string) bool {
	return name != "" && slices.ContainsFunc(o.Providers, func(p Provider) bool { return p.CredentialSecret == name })
}

// credentialPlace is where a credential's value is read from: an environment
// variable of the daemon's, or a file.
type credentialPlace struct {
	env, file string
}

func (p credentialPlace) String() string {
	if p.env != "" {
		return "environment variable " + p.env
	}
	return "file " + p.file
}

// placeOfFile names a file by its real path, so a second name for it (a
// symbolic link, a "..") is the same place.
func placeOfFile(f string) credentialPlace {
	if real, err := filepath.EvalSymlinks(f); err == nil {
		f = real
	}
	return credentialPlace{file: filepath.Clean(f)}
}

func placeOfSecret(s Secret) credentialPlace {
	if s.Env != "" {
		return credentialPlace{env: s.Env}
	}
	return placeOfFile(s.File)
}

// checkProviderCredentials holds each provider's credential to ADR-0057 point
// 3, which ADR-0058 point 1 moved from the channel to the provider: a secret a
// provider names is that provider's alone, and the place the value is read
// from, whether named directly or through a secret, holds nothing else pao
// reads: no other secret, provider or server. Otherwise a task naming the
// other secret, or a server given it, would be given the provider's
// credential. It runs once the secrets' files are resolved.
func checkProviderCredentials(path string, o Orchestrator) error {
	where := func(i int) string { return fmt.Sprintf("%s: provider %q", path, o.Providers[i].Name) }

	// Where each provider's credential is read from, by name.
	places := make([]credentialPlace, len(o.Providers))
	for i, c := range o.Providers {
		if c.CredentialEnv != "" {
			places[i] = credentialPlace{env: c.CredentialEnv}
			continue
		}
		s, ok := o.Secret(c.CredentialSecret)
		switch {
		case !ok:
			return fmt.Errorf("%s: no secret named %q is configured", where(i), c.CredentialSecret)
		case s.Inject != nil:
			return fmt.Errorf("%s: secret %q is injected through the egress proxy; a provider's secret is its own", where(i), c.CredentialSecret)
		case o.BoundToUpstream(c.CredentialSecret):
			return fmt.Errorf("%s: secret %q is already a server's credential", where(i), c.CredentialSecret)
		}
		for j := range i {
			if o.Providers[j].CredentialSecret == c.CredentialSecret {
				return fmt.Errorf("%s: secret %q is already the credential of provider %q", where(i), c.CredentialSecret, o.Providers[j].Name)
			}
		}
		places[i] = placeOfSecret(s)
	}

	// Everything else pao reads a credential from, and what it is.
	type reader struct {
		place credentialPlace
		what  string
	}
	var others []reader
	for _, s := range o.Secrets {
		others = append(others, reader{placeOfSecret(s), fmt.Sprintf("secret %q", s.Name)})
	}
	for _, u := range o.MCPUpstreams {
		if u.CredentialEnv != "" {
			others = append(others, reader{credentialPlace{env: u.CredentialEnv}, fmt.Sprintf("server %q's credential", u.Name)})
		}
	}
	for i, c := range o.Providers {
		others = append(others, reader{places[i], fmt.Sprintf("provider %q's credential", c.Name)})
	}

	for i, c := range o.Providers {
		for _, r := range others {
			if r.place != places[i] {
				continue
			}
			if r.what == fmt.Sprintf("provider %q's credential", c.Name) || (c.CredentialSecret != "" && r.what == fmt.Sprintf("secret %q", c.CredentialSecret)) {
				continue // the provider's own reference
			}
			return fmt.Errorf("%s: its credential is read from %s, which also holds %s; a provider's credential is its own (ADR-0057 point 3)", where(i), places[i], r.what)
		}
	}
	return nil
}
