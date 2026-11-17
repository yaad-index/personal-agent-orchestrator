// Package bot is a channel adapter for a chat service that offers a bot HTTP API
// with long polling. It is the pilot adapter of ADR-0019: it receives private
// messages, hands each to the layer with the service's own sender id, and sends
// pao's output to the humans the layer names.
//
// The API is spoken through a maintained client library. The endpoint, with the
// token's place in it, is configuration, and the library's own error text is never
// passed on: errors are described in the adapter's words, and the credential never
// appears in a log line or an error.
package bot

import (
	"context"
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

	api "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

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
	maxResponse         = 4 << 20
)

// The client library builds every request URL as <server>/bot<token>/<method>, so
// the endpoint template has to end that way; what comes before it is the server.
const endpointSuffix = "/bot{token}/{method}"

// Bot is the adapter.
type Bot struct {
	name  string
	token string
	h     adapter.Handler
	poll  time.Duration
	limit int
	api   *api.Bot

	mu     sync.Mutex
	last   map[string]core.Target // the agent or tag each sender last addressed
	runCtx context.Context        // the context Run was given, nil before Run
}

// New builds the adapter from its spec. The options are: endpoint (required), a URL
// template ending in /bot{token}/{method}; poll_timeout, in seconds; and
// message_limit, the longest message sent, in characters.
func New(spec adapter.Spec, h adapter.Handler) (adapter.Adapter, error) {
	return newBot(spec, h)
}

func newBot(spec adapter.Spec, h adapter.Handler) (*Bot, error) {
	ep, _ := spec.Options["endpoint"].(string)
	server, ok := strings.CutSuffix(ep, endpointSuffix)
	if !ok || strings.Contains(server, "{token}") || strings.Contains(server, "{method}") {
		return nil, errors.New(`option "endpoint" is required: a URL ending in ` + endpointSuffix)
	}
	if u, err := url.Parse(server); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
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
	if strings.TrimSpace(spec.Credential) == "" {
		return nil, errors.New("the channel has no credential")
	}
	b := &Bot{
		name: spec.Name, token: spec.Credential, h: h,
		poll: time.Duration(poll) * time.Second, limit: limit, last: map[string]core.Target{},
	}
	client := &http.Client{
		Timeout:   b.poll + 15*time.Second,
		Transport: capped{next: http.DefaultTransport, max: maxResponse},
	}
	// One worker handling each update before taking the next keeps a sender's
	// messages in order, which the sticky address depends on. The library asks for
	// a long poll one second shorter than the timeout it is given.
	b.api, err = api.New(spec.Credential,
		api.WithServerURL(server),
		api.WithHTTPClient(b.poll+time.Second, client),
		api.WithSkipGetMe(),
		api.WithAllowedUpdates(api.AllowedUpdates{"message"}),
		api.WithWorkers(1),
		api.WithNotAsyncHandlers(),
		api.WithUpdatesChannelCap(0),
		api.WithDefaultHandler(func(ctx context.Context, _ *api.Bot, u *models.Update) { b.handle(ctx, u) }),
		api.WithErrorsHandler(b.pollFailed),
	)
	if err != nil {
		return nil, b.describe(err)
	}
	return b, nil
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

// errTooLarge is a response past maxResponse.
var errTooLarge = errors.New("response too large")

// capped bounds how much of a response body is read.
type capped struct {
	next http.RoundTripper
	max  int64
}

func (c capped) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := c.next.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	resp.Body = &cappedBody{ReadCloser: resp.Body, left: c.max}
	return resp, nil
}

type cappedBody struct {
	io.ReadCloser
	left int64
}

func (c *cappedBody) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, errTooLarge
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.ReadCloser.Read(p)
	c.left -= int64(n)
	return n, err
}

// describe turns an error from the client library into the adapter's own words: the
// kind of failure and what the service said about it, never the library's text,
// the request URL or the token.
func (b *Bot) describe(err error) error {
	var (
		many    *api.TooManyRequestsError
		migrate *api.MigrateError
		ue      *url.Error
	)
	var msg string
	switch {
	case errors.Is(err, errTooLarge):
		msg = "the service's answer was too large"
	case errors.As(err, &many):
		msg = fmt.Sprintf("rate limited, retry after %d seconds", many.RetryAfter)
	case errors.As(err, &migrate):
		msg = "bad request: the chat moved"
	case errors.As(err, &ue):
		msg = "transport: " + ue.Err.Error()
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		msg = "cancelled"
	default:
		msg = "the service's answer could not be used"
		for _, k := range []error{api.ErrorUnauthorized, api.ErrorForbidden, api.ErrorNotFound, api.ErrorConflict, api.ErrorBadRequest} {
			if errors.Is(err, k) {
				msg = k.Error()
				if _, said, ok := strings.Cut(err.Error(), k.Error()+", "); ok {
					msg += ": " + truncate(said, 200)
				}
				break
			}
		}
	}
	if b.token != "" {
		msg = strings.ReplaceAll(msg, b.token, "***")
	}
	return errors.New(msg)
}

// pollFailed logs a failed poll. The library retries it with a growing pause, and
// honours a pause the service asks for. Nothing is logged once Run is stopping.
func (b *Bot) pollFailed(err error) {
	b.mu.Lock()
	ctx := b.runCtx
	b.mu.Unlock()
	if ctx != nil && ctx.Err() != nil {
		return
	}
	slog.Warn("channel poll failed", "channel", b.name, "error", b.describe(err))
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func native(chat int64, msg int) string {
	return strconv.FormatInt(chat, 10) + ":" + strconv.Itoa(msg)
}

// Run polls for updates until ctx is done, handling each before the next. An update
// is confirmed when the next poll is made, so a restart does not replay what was
// handled; the one being handled when the node died may not be replayed either.
func (b *Bot) Run(ctx context.Context) error {
	b.mu.Lock()
	b.runCtx = ctx
	b.mu.Unlock()
	b.api.Start(ctx)
	return nil
}

const usage = "address an agent: @name or #tag, then the message."

// handle acts on one update. A text from a person is considered. One from a group
// goes to the layer marked as such, which records and denies it (ADR-0019 point 6);
// anything without a person or text is not a message to a bot.
func (b *Bot) handle(ctx context.Context, u *models.Update) {
	if u == nil {
		return
	}
	m := u.Message
	if m == nil || m.From == nil || m.From.IsBot || m.From.ID == 0 || m.Text == "" {
		return
	}
	sender := strconv.FormatInt(m.From.ID, 10)
	in := comm.ChannelInbound{Channel: b.name, Sender: sender, Native: native(m.Chat.ID, m.ID), Raw: truncate(m.Text, 4096)}
	if m.Chat.Type != models.ChatTypePrivate {
		// A secret said in a group is not kept, even in the record of a denial.
		if _, reg := cutCommand(m.Text, "/register"); reg {
			in.Raw = ""
		}
		in.Group = true
		if _, err := b.h.ReceiveFromChannel(ctx, in); err != nil {
			slog.Error("channel could not record a rejected inbound", "channel", b.name, "error", err)
		}
		return
	}
	if m.ReplyToMessage != nil {
		in.ReplyNative = native(m.Chat.ID, m.ReplyToMessage.ID)
	}

	explicit := false
	if rest, ok := cutCommand(m.Text, "/register"); ok {
		in.Registration, in.Text = true, strings.TrimSpace(rest)
	} else {
		in.Target, in.Text, explicit = parseAddress(m.Text)
		if !explicit && m.ReplyToMessage == nil {
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
func (b *Bot) send(ctx context.Context, chat int64, text string) (int, error) {
	m, err := b.api.SendMessage(ctx, &api.SendMessageParams{ChatID: chat, Text: text})
	if err != nil {
		return 0, b.describe(err)
	}
	return m.ID, nil
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
