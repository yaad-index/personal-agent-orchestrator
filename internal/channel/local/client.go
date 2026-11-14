package local

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Client talks to a local channel server.
type Client struct {
	// URL is the server's base URL, e.g. http://127.0.0.1:7744.
	URL string
	// Token is the bearer token.
	Token string
	// HTTP is the client used; nil means http.DefaultClient.
	HTTP *http.Client
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// Post sends a message to the target and returns the server's delivery report.
func (c *Client) Post(ctx context.Context, sender, text string, kind, value string) (DeliveryReport, error) {
	return c.PostReply(ctx, sender, text, kind, value, "", "")
}

// PostReply sends a message that answers another: by the id of that message, as the
// stream showed it (qualified by its node in a mesh), or by a snippet of its text.
// Give one of them, or neither for a message that answers nothing.
func (c *Client) PostReply(ctx context.Context, sender, text, kind, value, reply, replyQuote string) (DeliveryReport, error) {
	body, err := json.Marshal(postMessageRequest{Sender: sender, Text: text, Target: targetJSON{Kind: kind, Value: value}, Reply: reply, ReplyQuote: replyQuote})
	if err != nil {
		return DeliveryReport{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return DeliveryReport{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.http().Do(req)
	if err != nil {
		return DeliveryReport{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return DeliveryReport{}, apiError(resp)
	}
	var out DeliveryReport
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return DeliveryReport{}, err
	}
	return out, nil
}

// Decide answers a gate question: decision is "allow", "always" or "deny".
func (c *Client) Decide(ctx context.Context, id, decision, reason string) error {
	body, err := json.Marshal(decisionRequest{ID: id, Decision: decision, Reason: reason})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/v1/decisions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return apiError(resp)
	}
	return nil
}

// DeliveryReport is the server's answer to a posted message.
type DeliveryReport = deliveryJSON

// Result is the per-agent outcome inside a DeliveryReport.
type Result = resultJSON

// Stream calls fn for every message the server sends until ctx is done or the
// stream ends. It returns nil when ctx ended it.
func (c *Client) Stream(ctx context.Context, fn func(MessageJSON)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+"/v1/stream", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.http().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return apiError(resp)
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var m MessageJSON
		if err := json.Unmarshal([]byte(data), &m); err != nil {
			continue
		}
		fn(m)
	}
	if ctx.Err() != nil {
		return nil
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return io.EOF
}

func apiError(resp *http.Response) error {
	var body struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&body)
	if body.Error == "" {
		body.Error = http.StatusText(resp.StatusCode)
	}
	return fmt.Errorf("%d: %s", resp.StatusCode, body.Error)
}
