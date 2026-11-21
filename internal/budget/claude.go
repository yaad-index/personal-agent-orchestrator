package budget

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"
)

// UsageURL is where a subscription account of the coding-agent harness reports
// its usage.
const UsageURL = "https://api.anthropic.com/api/oauth/usage"

// ClaudeSubscription reads a subscription account of the coding-agent harness:
// the share used of its weekly and five-hour windows, which the account reports
// for the whole plan, every agent on that login together.
//
// It reads the login's access token from the harness's credentials file on
// every read and never refreshes it: the harness refreshes its own token, and a
// second refresher would rotate the refresh token out from under it. A token
// the endpoint refuses is a fault until the file holds one it accepts.
type ClaudeSubscription struct {
	// Credentials is the path of the harness's credentials file for the login.
	Credentials string
	// URL is the usage endpoint; empty means UsageURL.
	URL string
	// Client is the HTTP client; nil means one with a 15s timeout.
	Client *http.Client
}

type usageWindow struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    string   `json:"resets_at"`
}

// Read returns the account's figure. Its errors name what failed, never the
// token or the response body.
func (c ClaudeSubscription) Read(ctx context.Context) (Figure, error) {
	token, err := c.token()
	if err != nil {
		return Figure{}, err
	}
	endpoint := c.URL
	if endpoint == "" {
		endpoint = UsageURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Figure{}, fmt.Errorf("usage request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		// The URL is the only part of the request in a transport error.
		return Figure{}, &TransientError{Err: fmt.Errorf("usage request failed: %w", unwrapURL(err))}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		switch {
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			return Figure{}, fmt.Errorf("usage endpoint refused the login's token (HTTP %d); it is read again from the credentials file on the next poll", resp.StatusCode)
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			// A rate limit or a server error passes: the reading is kept while
			// the read is tried again later.
			return Figure{}, &TransientError{Err: fmt.Errorf("usage endpoint answered HTTP %d", resp.StatusCode), RetryAfter: retryAfter(resp.Header.Get("Retry-After"), time.Now())}
		}
		return Figure{}, fmt.Errorf("usage endpoint answered HTTP %d", resp.StatusCode)
	}
	var body struct {
		FiveHour *usageWindow `json:"five_hour"`
		SevenDay *usageWindow `json:"seven_day"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return Figure{}, errors.New("usage response is not the expected JSON")
	}
	week, err := window("seven_day", body.SevenDay)
	if err != nil {
		return Figure{}, err
	}
	five, err := window("five_hour", body.FiveHour)
	if err != nil {
		return Figure{}, err
	}
	return Figure{Week: week, FiveHour: five}, nil
}

// window turns a reported window into the share left. A window the response
// leaves out, or reports with no utilization, is a fault: nothing is assumed of
// a figure that was not given (ADR-0035 point 1).
func window(name string, w *usageWindow) (Window, error) {
	if w == nil || w.Utilization == nil {
		return Window{}, fmt.Errorf("usage response has no %s utilization", name)
	}
	u := *w.Utilization
	if u < 0 || u > 100 {
		return Window{}, fmt.Errorf("usage response %s utilization %v is outside 0-100", name, u)
	}
	out := Window{Left: 100 - u}
	if w.ResetsAt != "" {
		t, err := time.Parse(time.RFC3339Nano, w.ResetsAt)
		if err != nil {
			return Window{}, fmt.Errorf("usage response %s resets_at is not a time", name)
		}
		out.Resets = t
	}
	return out, nil
}

func (c ClaudeSubscription) token() (string, error) {
	b, err := os.ReadFile(c.Credentials)
	if err != nil {
		return "", fmt.Errorf("credentials file: %w", errors.Unwrap(err))
	}
	var f struct {
		OAuth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return "", errors.New("credentials file is not the expected JSON")
	}
	if f.OAuth.AccessToken == "" {
		return "", errors.New("credentials file holds no access token")
	}
	return f.OAuth.AccessToken, nil
}

func unwrapURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// retryAfter reads a Retry-After header, in seconds or as an HTTP date, as a
// wait from now. Anything else, or a time already past, is no wait.
func retryAfter(v string, now time.Time) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}
