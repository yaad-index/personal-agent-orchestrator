// Package bot is a channel adapter for a chat service that offers a bot HTTP API
// with long polling. It is the pilot adapter of ADR-0019: it receives private
// messages, hands each to the layer with the service's own sender id, and sends
// pao's output to the humans the layer names.
//
// Nothing here knows which service it talks to. The endpoint, with the token's
// place in it, is configuration; the credential never appears in a log line or an
// error.
package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/channel/adapter"
	"github.com/yaad-index/personal-agent-orchestrator/internal/comm"
	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// Defaults for the options.
const (
	DefaultPollTimeout  = 30 * time.Second
	DefaultMessageLimit = 4000
	maxPollTimeout      = 60 * time.Second
	minMessageLimit     = 100
	startBackoff        = time.Second
	maxBackoff          = time.Minute
)

// Bot is the adapter.
type Bot struct {
	name     string
	endpoint string
	token    string
	h        adapter.Handler
	client   *http.Client
	poll     time.Duration
	limit    int

	// backoff bounds, settable for tests.
	startBackoff, maxBackoff time.Duration
	// wait pauses between retries; it reports false when ctx ended first.
	wait func(ctx context.Context, d time.Duration) bool

	mu   sync.Mutex
	last map[string]core.Target // the agent or tag each sender last addressed
}

// New builds the adapter from its spec. The options are: endpoint (required), a URL
// template containing {token} and {method}; poll_timeout, in seconds; and
// message_limit, the longest message sent, in characters.
func New(spec adapter.Spec, h adapter.Handler) (adapter.Adapter, error) {
	return newBot(spec, h, nil)
}

func newBot(spec adapter.Spec, h adapter.Handler, client *http.Client) (*Bot, error) {
	ep, _ := spec.Options["endpoint"].(string)
	if ep == "" || !strings.Contains(ep, "{method}") || !strings.Contains(ep, "{token}") {
		return nil, errors.New(`option "endpoint" is required: a URL with {token} and {method} in it`)
	}
	if u, err := url.Parse(strings.NewReplacer("{token}", "x", "{method}", "x").Replace(ep)); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, errors.New(`option "endpoint" is not an http or https URL`)
	}
	poll, err := intOption(spec.Options, "poll_timeout", int(DefaultPollTimeout/time.Second))
	if err != nil {
		return nil, err
	}
	if poll < 0 || time.Duration(poll)*time.Second > maxPollTimeout {
		return nil, fmt.Errorf(`option "poll_timeout" must be 0 to %d seconds`, int(maxPollTimeout/time.Second))
	}
	limit, err := intOption(spec.Options, "message_limit", DefaultMessageLimit)
	if err != nil {
		return nil, err
	}
	if limit < minMessageLimit {
		return nil, fmt.Errorf(`option "message_limit" must be at least %d`, minMessageLimit)
	}
	for k := range spec.Options {
		switch k {
		case "endpoint", "poll_timeout", "message_limit":
		default:
			return nil, fmt.Errorf("unknown option %q", k)
		}
	}
	if client == nil {
		client = &http.Client{Timeout: time.Duration(poll)*time.Second + 15*time.Second}
	}
	return &Bot{
		name: spec.Name, endpoint: ep, token: spec.Credential, h: h, client: client,
		poll: time.Duration(poll) * time.Second, limit: limit,
		startBackoff: startBackoff, maxBackoff: maxBackoff, last: map[string]core.Target{}, wait: sleep,
	}, nil
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func intOption(o map[string]any, key string, def int) (int, error) {
	v, ok := o[key]
	if !ok {
		return def, nil
	}
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case uint64:
		return int(n), nil
	case float64:
		if n == float64(int(n)) {
			return int(n), nil
		}
	}
	return 0, fmt.Errorf("option %q must be a whole number", key)
}

// Name is the channel's name.
func (b *Bot) Name() string { return b.name }

type apiReply struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
}

// call posts a JSON body to a method of the API and returns its result. Any error
// is scrubbed of the token, which sits in the URL the standard client reports.
func (b *Bot) call(ctx context.Context, method string, body, into any) error {
	u := strings.NewReplacer("{token}", b.token, "{method}", method).Replace(b.endpoint)
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(raw))
	if err != nil {
		return b.scrub(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return b.scrub(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return b.scrub(err)
	}
	var out apiReply
	_ = json.Unmarshal(data, &out)
	if resp.StatusCode/100 != 2 || !out.OK {
		return b.scrub(fmt.Errorf("%s: status %d: %s", method, resp.StatusCode, truncate(out.Description, 200)))
	}
	if into != nil {
		if err := json.Unmarshal(out.Result, into); err != nil {
			return b.scrub(fmt.Errorf("%s: unreadable result", method))
		}
	}
	return nil
}

// scrub removes the token from an error.
func (b *Bot) scrub(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	if b.token == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), b.token, "***"))
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

type update struct {
	ID      int64    `json:"update_id"`
	Message *message `json:"message"`
}

type message struct {
	ID   int64 `json:"message_id"`
	From *struct {
		ID    int64 `json:"id"`
		IsBot bool  `json:"is_bot"`
	} `json:"from"`
	Chat struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
	Text    string `json:"text"`
	ReplyTo *struct {
		ID int64 `json:"message_id"`
	} `json:"reply_to_message"`
}

func native(chat, msg int64) string {
	return strconv.FormatInt(chat, 10) + ":" + strconv.FormatInt(msg, 10)
}

// Run polls for updates until ctx is done. A failed poll is retried with a growing
// pause; an update is confirmed as soon as it has been handled, so a restart does
// not replay what was done, though one the node died in the middle of is replayed.
func (b *Bot) Run(ctx context.Context) error {
	var offset int64
	pause := b.startBackoff
	for ctx.Err() == nil {
		var ups []update
		err := b.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": int(b.poll / time.Second), "allowed_updates": []string{"message"}}, &ups)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			slog.Warn("channel poll failed", "channel", b.name, "error", err)
			if !b.wait(ctx, pause) {
				return nil
			}
			if pause *= 2; pause > b.maxBackoff {
				pause = b.maxBackoff
			}
			continue
		}
		pause = b.startBackoff
		for _, u := range ups {
			b.handle(ctx, u)
			if u.ID >= offset {
				offset = u.ID + 1
			}
		}
	}
	return nil
}

const usage = "address an agent: @name or #tag, then the message."

// handle acts on one update. Only a private message with text from a person is
// considered: a group is denied by default (ADR-0019 point 6), and anything else is
// not a message to a bot.
func (b *Bot) handle(ctx context.Context, u update) {
	m := u.Message
	if m == nil || m.From == nil || m.From.IsBot || m.From.ID == 0 || m.Chat.Type != "private" || m.Text == "" {
		return
	}
	sender := strconv.FormatInt(m.From.ID, 10)
	in := comm.ChannelInbound{Channel: b.name, Sender: sender, Native: native(m.Chat.ID, m.ID), Raw: truncate(m.Text, 4096)}
	if m.ReplyTo != nil {
		in.ReplyNative = native(m.Chat.ID, m.ReplyTo.ID)
	}

	explicit := false
	if rest, ok := cutCommand(m.Text, "/register"); ok {
		in.Registration, in.Text = true, strings.TrimSpace(rest)
	} else {
		in.Target, in.Text, explicit = parseAddress(m.Text)
		if !explicit && m.ReplyTo == nil {
			b.mu.Lock()
			in.Target = b.last[sender]
			b.mu.Unlock()
		}
	}

	res, err := b.h.ReceiveFromChannel(ctx, in)
	switch {
	case errors.Is(err, comm.ErrRecord):
		slog.Error("channel could not record a rejected inbound", "channel", b.name, "error", err)
	case errors.Is(err, core.ErrNoTarget):
		b.reply(ctx, m.Chat.ID, usage)
	case err != nil:
		b.reply(ctx, m.Chat.ID, truncate(err.Error(), 400))
	default:
		if explicit && res.Delivery != nil {
			b.mu.Lock()
			b.last[sender] = in.Target
			b.mu.Unlock()
		}
		if res.Reply != "" {
			b.reply(ctx, m.Chat.ID, res.Reply)
		}
	}
}

// cutCommand reports whether text is the command, alone or followed by a space and
// more, and returns the rest. The command may carry the bot's own name after an "@",
// as a chat client writes it in a group.
func cutCommand(text, cmd string) (string, bool) {
	head, tail, _ := strings.Cut(text, " ")
	if head == cmd || strings.HasPrefix(head, cmd+"@") {
		return tail, true
	}
	return "", false
}

// parseAddress reads a leading "@name" or "#tag" off a message. explicit is false,
// and text unchanged, when there is none.
func parseAddress(text string) (t core.Target, rest string, explicit bool) {
	text = strings.TrimSpace(text)
	head, tail, _ := strings.Cut(text, " ")
	if len(head) < 2 {
		return core.Target{}, text, false
	}
	switch head[0] {
	case '@':
		return core.Target{Kind: core.ByName, Value: head[1:]}, strings.TrimSpace(tail), true
	case '#':
		return core.Target{Kind: core.ByTag, Value: head[1:]}, strings.TrimSpace(tail), true
	}
	return core.Target{}, text, false
}

func (b *Bot) reply(ctx context.Context, chat int64, text string) {
	if _, err := b.send(ctx, chat, text); err != nil {
		slog.Warn("channel could not reply", "channel", b.name, "error", err)
	}
}

// send posts one message to a chat and returns the service's id for it.
func (b *Bot) send(ctx context.Context, chat int64, text string) (int64, error) {
	var out struct {
		ID int64 `json:"message_id"`
	}
	if err := b.call(ctx, "sendMessage", map[string]any{"chat_id": chat, "text": text}, &out); err != nil {
		return 0, err
	}
	return out.ID, nil
}

// Send hands a message to the humans pao's output goes to.
func (b *Bot) Send(ctx context.Context, m core.Message) error {
	_, err := b.SendNative(ctx, m)
	return err
}

// SendNative is Send, returning the native id of the first piece sent to each
// recipient, so a reply to it can be found again.
func (b *Bot) SendNative(ctx context.Context, m core.Message) ([]string, error) {
	var natives []string
	var errs []error
	pieces := split(m.Line(), b.limit)
	for _, to := range b.h.Recipients(b.name) {
		chat, err := strconv.ParseInt(to, 10, 64)
		if err != nil {
			errs = append(errs, fmt.Errorf("recipient %q is not a chat id", to))
			continue
		}
		for i, p := range pieces {
			id, err := b.send(ctx, chat, p)
			if err != nil {
				errs = append(errs, err)
				break
			}
			if i == 0 {
				natives = append(natives, native(chat, id))
			}
		}
	}
	return natives, errors.Join(errs...)
}

// split cuts text into pieces of at most limit characters, at a line or word
// boundary where there is one.
func split(text string, limit int) []string {
	r := []rune(text)
	var out []string
	for len(r) > limit {
		cut := limit
		for i := limit; i > limit/2; i-- {
			if r[i] == '\n' {
				cut = i
				break
			}
			if r[i] == ' ' && cut == limit {
				cut = i
			}
		}
		out = append(out, strings.TrimRight(string(r[:cut]), " \n"))
		r = []rune(strings.TrimLeft(string(r[cut:]), " \n"))
	}
	if len(r) > 0 || len(out) == 0 {
		out = append(out, string(r))
	}
	return out
}
