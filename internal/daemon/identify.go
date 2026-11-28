package daemon

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/config"
	"github.com/yaad-index/personal-agent-orchestrator/internal/identity"
)

const (
	// identifyTTL is how long a code sent to a target can be entered.
	identifyTTL = 10 * time.Minute
	// identifyTries is how many wrong codes end one, so a code cannot be
	// guessed by trying.
	identifyTries = 5
	// codeAlphabet leaves out letters and digits read as one another.
	codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	codeLength   = 8
)

// where is what a code shows the operator controls: one target, of one owner,
// on one provider (ADR-0058 point 2).
type where struct{ provider, target, owner string }

func whereOf(c config.Channel) where { return where{c.Provider, c.Target, c.Owner} }

// identification is a code sent for a channel and not yet entered.
type identification struct {
	at      where
	hash    [32]byte
	expires time.Time
	tries   int
}

// identifyCodes holds the codes sent to targets, by channel name: one each, the latest.
// A code is used once, and is kept in memory only, so a restart ends them all.
type identifyCodes struct {
	now func() time.Time
	mu  sync.Mutex
	by  map[string]identification
}

func newIdentifyCodes(now func() time.Time) *identifyCodes {
	return &identifyCodes{now: now, by: map[string]identification{}}
}

// issue makes a new code for channel at at, replacing any before it, and
// returns it with its expiry.
func (c *identifyCodes) issue(channel string, at where) (string, time.Time, error) {
	b := make([]byte, codeLength)
	if _, err := rand.Read(b); err != nil {
		return "", time.Time{}, err
	}
	for i := range b {
		b[i] = codeAlphabet[int(b[i])%len(codeAlphabet)]
	}
	code := string(b[:codeLength/2]) + "-" + string(b[codeLength/2:])
	c.mu.Lock()
	defer c.mu.Unlock()
	exp := c.now().Add(identifyTTL)
	c.by[channel] = identification{at: at, hash: sha256.Sum256([]byte(normalCode(code))), expires: exp}
	return code, exp, nil
}

// drop forgets channel's code, as when it could not be sent.
func (c *identifyCodes) drop(channel string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.by, channel)
}

// redeem uses channel's code, if code is it, it has not expired, and it was
// sent to at. A wrong code counts against it; the last allowed one ends it.
func (c *identifyCodes) redeem(channel string, at where, code string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	id, ok := c.by[channel]
	switch {
	case strings.TrimSpace(code) == "":
		return fmt.Errorf("channel %q needs its target identified first: ask pao to send a code there, and enter the code", channel)
	case !ok:
		return fmt.Errorf("no code is waiting for channel %q: ask pao to send one", channel)
	case c.now().After(id.expires):
		delete(c.by, channel)
		return fmt.Errorf("the code for channel %q has expired: ask pao to send another", channel)
	case id.at != at:
		return fmt.Errorf("the code for channel %q was sent to another target, owner or provider: ask pao to send one for this one", channel)
	}
	h := sha256.Sum256([]byte(normalCode(code)))
	if subtle.ConstantTimeCompare(h[:], id.hash[:]) != 1 {
		id.tries++
		if id.tries >= identifyTries {
			delete(c.by, channel)
			return fmt.Errorf("that is not the code, and too many were wrong: ask pao to send another")
		}
		c.by[channel] = id
		return fmt.Errorf("that is not the code pao sent for channel %q", channel)
	}
	delete(c.by, channel)
	return nil
}

// normalCode reads a code as typed: any case, with or without its dash or
// spaces.
func normalCode(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || r == ' ' {
			return -1
		}
		return r
	}, strings.ToUpper(strings.TrimSpace(s)))
}

// sendersOn lists the sender ids bound to a subject on a provider.
func sendersOn(ids *identity.Store) func(provider, subject string) ([]string, error) {
	return func(provider, subject string) ([]string, error) {
		bs, err := ids.Bindings(subject)
		if err != nil {
			return nil, err
		}
		var out []string
		for _, b := range bs {
			if b.Channel == provider {
				out = append(out, b.Sender)
			}
		}
		return out, nil
	}
}

// identifyText is what is sent to a target with its code.
func identifyText(channel, code string, ttl time.Duration) string {
	return fmt.Sprintf("pao: to make this conversation channel %q, enter this code where it was asked for: %s\nIt can be used once, within %d minutes. If nobody asked for it, ignore this message.", channel, code, int(ttl/time.Minute))
}
