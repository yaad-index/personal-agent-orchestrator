// Package bot is a channel adapter for a chat service that offers a bot HTTP API
// with long polling. It is the pilot adapter of ADR-0019: it receives messages from
// private and group conversations, hands each to the layer with the service's own
// sender id and, for a group, the conversation's id, and sends pao's output to the
// humans the layer names. Which groups are served is the layer's to say (ADR-0028). A message's actions are shown as
// buttons under it; a press comes back to the layer as the command the button
// carries, and the buttons are closed when the layer says the message is settled
// (ADR-0027).
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
	"unicode/utf8"

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
	// maxPayload is the most a button can carry, in bytes. An action whose
	// command is longer gets no button; it stays a typed command.
	maxPayload = 64
	// maxPressAnswer is the longest answer to a press, in characters.
	maxPressAnswer = 200
	// pressAnsweredPrivately is the answer to a press whose reply was sent to the
	// presser's private chat instead.
	pressAnsweredPrivately = "done: the answer is in your private chat with the bot"
	// maxShown is how many messages' buttons are kept for closing. Past it, the
	// oldest are forgotten, and a press on one decides nothing anyway.
	maxShown = 256
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

	self int64 // the bot's own user id, read from its credential; 0 if unknown

	mu     sync.Mutex
	last   map[string]core.Target // the agent or tag each sender last addressed, by conversation and sender
	runCtx context.Context        // the context Run was given, nil before Run
	shown  map[string][]shown     // the copies with buttons, by pao message id
	order  []string               // the keys of shown, oldest first
}

// shown is one copy of a message that carries buttons: where it is, and its text,
// to show again with the outcome when it is closed.
type shown struct {
	chat int64
	id   int
	text string
}

// New builds the adapter from its spec. The options are: endpoint (required), a URL
// template ending in /bot{token}/{method}; poll_timeout, in seconds; and
// message_limit, the longest message sent, in characters.
func New(spec adapter.Spec, h adapter.Handler) (adapter.Adapter, error) {
	return newBot(spec, h)
}

// CheckOptions validates a channel's options as New does, without a credential
// and without the network.
func CheckOptions(o map[string]any) error {
	_, err := options(o)
	return err
}

// settings are the options, validated.
type settings struct {
	server string
	poll   time.Duration
	limit  int
}

func options(o map[string]any) (settings, error) {
	ep, _ := o["endpoint"].(string)
	server, ok := strings.CutSuffix(ep, endpointSuffix)
	if !ok || strings.Contains(server, "{token}") || strings.Contains(server, "{method}") {
		return settings{}, errors.New(`option "endpoint" is required: a URL ending in ` + endpointSuffix)
	}
	if u, err := url.Parse(server); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return settings{}, errors.New(`option "endpoint" is not an http or https URL`)
	}
	poll, err := intOption(o, "poll_timeout", int(DefaultPollTimeout/time.Second))
	if err != nil {
		return settings{}, err
	}
	if poll < 0 || time.Duration(poll)*time.Second > maxPollTimeout {
		return settings{}, fmt.Errorf(`option "poll_timeout" must be 0 to %d seconds`, int(maxPollTimeout/time.Second))
	}
	limit, err := intOption(o, "message_limit", DefaultMessageLimit)
	if err != nil {
		return settings{}, err
	}
	if limit < minMessageLimit {
		return settings{}, fmt.Errorf(`option "message_limit" must be at least %d`, minMessageLimit)
	}
	for k := range o {
		switch k {
		case "endpoint", "poll_timeout", "message_limit":
		default:
			return settings{}, fmt.Errorf("unknown option %q", k)
		}
	}
	return settings{server: server, poll: time.Duration(poll) * time.Second, limit: limit}, nil
}

func newBot(spec adapter.Spec, h adapter.Handler) (*Bot, error) {
	set, err := options(spec.Options)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(spec.Credential) == "" {
		return nil, errors.New("the channel has no credential")
	}
	b := &Bot{
		name: spec.Name, token: spec.Credential, h: h,
		poll: set.poll, limit: set.limit, last: map[string]core.Target{},
		shown: map[string][]shown{}, self: selfID(spec.Credential),
	}
	client := &http.Client{
		Timeout:   b.poll + 15*time.Second,
		Transport: capped{next: http.DefaultTransport, max: maxResponse},
	}
	// One worker handling each update before taking the next keeps a sender's
	// messages in order, which the sticky address depends on. The library asks for
	// a long poll one second shorter than the timeout it is given.
	b.api, err = api.New(spec.Credential,
		api.WithServerURL(set.server),
		api.WithHTTPClient(b.poll+time.Second, client),
		api.WithSkipGetMe(),
		api.WithAllowedUpdates(api.AllowedUpdates{"message", "callback_query"}),
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

// selfID reads the bot's own user id from its credential, which the service
// issues as "<id>:<secret>". It is 0 when the credential is not of that form.
func selfID(credential string) int64 {
	head, _, ok := strings.Cut(credential, ":")
	if !ok {
		return 0
	}
	id, err := strconv.ParseInt(head, 10, 64)
	if err != nil {
		return 0
	}
	return id
}

// lastKey is where a sender's last address is kept: per sender and per
// conversation, so an address made in a group does not steer what the same person
// types privately (ADR-0028 point 4).
func lastKey(chat int64, sender string) string {
	return strconv.FormatInt(chat, 10) + "/" + sender
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

const usage = "address an agent: /pao agent <name> <message>, or see /pao help."

// handle acts on one update. A text from a person is considered, from a private
// conversation or a group alike; the layer decides whether a group is served. In
// a group, the service's reports that the conversation moved to a new id, and that
// someone joined it, go to the layer too, to be recorded. Anything else without a
// person or text is not a message to a bot.
func (b *Bot) handle(ctx context.Context, u *models.Update) {
	if u == nil {
		return
	}
	if u.CallbackQuery != nil {
		b.press(ctx, u.CallbackQuery)
		return
	}
	m := u.Message
	if m == nil {
		return
	}
	group := m.Chat.Type != models.ChatTypePrivate
	if group && b.notice(ctx, m) {
		return
	}
	if m.From == nil || m.From.IsBot || m.From.ID == 0 || m.Text == "" {
		return
	}
	sender := strconv.FormatInt(m.From.ID, 10)
	in := comm.ChannelInbound{Channel: b.name, Sender: sender, Native: native(m.Chat.ID, m.ID), Raw: truncate(m.Text, 4096)}
	if group {
		in.Group, in.Conversation = true, strconv.FormatInt(m.Chat.ID, 10)
	}
	if m.ReplyToMessage != nil {
		in.ReplyNative = native(m.Chat.ID, m.ReplyToMessage.ID)
	}

	if rest, ok := cutCommand(m.Text, "/register"); ok {
		in.Registration, in.Text = true, strings.TrimSpace(rest)
		if group {
			// A secret said in a group is never kept or tried, even in the record
			// of a denial (ADR-0028 point 3).
			in.Text, in.Raw = "", ""
		}
	} else {
		// A /pao command names its own target, or none; the layer reads it. Any other
		// text goes where the sender last addressed here, unless it answers a message.
		in.Text = m.Text
		if m.ReplyToMessage == nil {
			b.mu.Lock()
			in.Target = b.last[lastKey(m.Chat.ID, sender)]
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
		if res.Addressed != nil && res.Delivery != nil {
			b.mu.Lock()
			b.last[lastKey(m.Chat.ID, sender)] = *res.Addressed
			b.mu.Unlock()
		}
		if res.Reply != "" {
			b.reply(ctx, m.Chat.ID, res.Reply)
		}
	}
}

// notice hands the layer a group's service report, if m is one, and reports whether
// it was: the conversation moving to a new id, or people joining it. Who joined is
// known only where the service reports it; someone added without a report is not
// seen (ADR-0028 point 5).
func (b *Bot) notice(ctx context.Context, m *models.Message) bool {
	chat := strconv.FormatInt(m.Chat.ID, 10)
	var ins []comm.ChannelInbound
	switch {
	case m.MigrateToChatID != 0:
		ins = append(ins, comm.ChannelInbound{Sender: chat, Conversation: chat, MovedTo: strconv.FormatInt(m.MigrateToChatID, 10)})
	case m.MigrateFromChatID != 0:
		from := strconv.FormatInt(m.MigrateFromChatID, 10)
		ins = append(ins, comm.ChannelInbound{Sender: from, Conversation: from, MovedTo: chat})
	case len(m.NewChatMembers) > 0:
		for _, u := range m.NewChatMembers {
			if u.ID == 0 || u.ID == b.self {
				continue
			}
			ins = append(ins, comm.ChannelInbound{Sender: strconv.FormatInt(u.ID, 10), Conversation: chat, Join: true})
		}
	default:
		return false
	}
	for _, in := range ins {
		in.Channel, in.Group = b.name, true
		if _, err := b.h.ReceiveFromChannel(ctx, in); err != nil {
			slog.Error("channel could not record a rejected inbound", "channel", b.name, "error", err)
		}
	}
	return true
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

// fitsPressAnswer reports whether a reply fits the brief notice a press is
// answered with: one line of at most maxPressAnswer characters.
func fitsPressAnswer(s string) bool {
	return !strings.Contains(s, "\n") && utf8.RuneCountInString(s) <= maxPressAnswer
}

func (b *Bot) reply(ctx context.Context, chat int64, text string) {
	if _, err := b.send(ctx, chat, text, nil); err != nil {
		slog.Warn("channel could not reply", "channel", b.name, "error", err)
	}
}

// send posts one message to a chat, with buttons when keys is set, and returns the
// service's id for it.
func (b *Bot) send(ctx context.Context, chat int64, text string, keys models.ReplyMarkup) (int, error) {
	m, err := b.api.SendMessage(ctx, &api.SendMessageParams{ChatID: chat, Text: text, ReplyMarkup: keys})
	if err != nil {
		return 0, b.describe(err)
	}
	return m.ID, nil
}

// Send hands a message to the conversations the layer names.
func (b *Bot) Send(ctx context.Context, m core.Message) error {
	_, err := b.SendNative(ctx, m)
	return err
}

// SendNative is Send, returning the native id of the first piece sent to each
// conversation, so a reply to it can be found again. The message's actions are
// buttons under its last piece, in a group as in a private conversation.
func (b *Bot) SendNative(ctx context.Context, m core.Message) ([]string, error) {
	var natives []string
	var errs []error
	pieces := split(b.line(m), b.limit)
	keys := keyboard(m.Actions)
	for _, to := range b.h.Destinations(b.name, m) {
		chat, err := strconv.ParseInt(to, 10, 64)
		if err != nil {
			errs = append(errs, fmt.Errorf("destination %q is not a chat id", to))
			continue
		}
		for i, p := range pieces {
			var markup models.ReplyMarkup
			if keys != nil && i == len(pieces)-1 {
				markup = keys
			}
			id, err := b.send(ctx, chat, p, markup)
			if err != nil {
				errs = append(errs, err)
				break
			}
			if i == 0 {
				natives = append(natives, native(chat, id))
			}
			if markup != nil {
				b.remember(m.ID, shown{chat: chat, id: id, text: p})
			}
		}
	}
	return natives, errors.Join(errs...)
}

// line is the text a message is shown as: its sender's name before it, except for
// the channel's default agent, which speaks as the channel's own voice (ADR-0023
// point 5).
func (b *Bot) line(m core.Message) string {
	if agent, ok := b.h.DefaultAgent(b.name); ok && agent == m.Sender && m.Label == "" {
		return m.Text
	}
	return m.Shown()
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

// keyboard draws actions as one row of buttons, each carrying its command. An
// action without a label, or whose command does not fit in a button, gets none.
// It is nil when no action gets a button.
func keyboard(actions []core.Action) *models.InlineKeyboardMarkup {
	var row []models.InlineKeyboardButton
	for _, a := range actions {
		if a.Label == "" || a.Command == "" || len(a.Command) > maxPayload {
			continue
		}
		row = append(row, models.InlineKeyboardButton{Text: a.Label, CallbackData: a.Command})
	}
	if len(row) == 0 {
		return nil
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{row}}
}

// remember keeps a copy with buttons for closing, forgetting the oldest message
// past maxShown.
func (b *Bot) remember(id string, c shown) {
	if id == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.shown[id]; !ok {
		b.order = append(b.order, id)
		if len(b.order) > maxShown {
			delete(b.shown, b.order[0])
			b.order = b.order[1:]
		}
	}
	b.shown[id] = append(b.shown[id], c)
}

// CloseActions removes the buttons from every copy of message id and shows the
// outcome under its text. A message whose buttons were not shown here, or were
// already closed, is left alone.
func (b *Bot) CloseActions(ctx context.Context, id, outcome string) error {
	b.mu.Lock()
	copies := b.shown[id]
	delete(b.shown, id)
	for i, k := range b.order {
		if k == id {
			b.order = append(b.order[:i:i], b.order[i+1:]...)
			break
		}
	}
	b.mu.Unlock()
	var errs []error
	for _, c := range copies {
		_, err := b.api.EditMessageText(ctx, &api.EditMessageTextParams{
			ChatID: c.chat, MessageID: c.id, Text: closed(c.text, outcome, b.limit),
			ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{}},
		})
		if err != nil {
			errs = append(errs, b.describe(err))
		}
	}
	return errors.Join(errs...)
}

// closed is a copy's text with its outcome under it, kept within limit.
func closed(text, outcome string, limit int) string {
	tail := "\n\n— " + outcome
	if r := []rune(text); len(r)+len([]rune(tail)) > limit {
		text = string(r[:max(0, limit-len([]rune(tail))-1)]) + "…"
	}
	return text + tail
}

// press hands a button press to the layer as the command the button carries,
// from the service's authenticated sender of the press, and answers the press
// with the layer's reply, which only the presser sees (ADR-0027 points 4 and 5).
//
// The answer to a press is a notice the service shows briefly over the chat, one
// short line. A reply that does not fit one, such as an agent list, is sent in
// full as a message to the presser's private chat with the bot, and the notice
// says where it went. A reply that cannot be sent there falls back to the notice,
// cut short.
func (b *Bot) press(ctx context.Context, q *models.CallbackQuery) {
	answer := ""
	defer func() {
		if !fitsPressAnswer(answer) {
			if _, err := b.send(ctx, q.From.ID, answer, nil); err != nil {
				slog.Warn("channel could not send a press's answer to the presser's private chat", "channel", b.name, "error", err)
			} else {
				answer = pressAnsweredPrivately
			}
		}
		if _, err := b.api.AnswerCallbackQuery(ctx, &api.AnswerCallbackQueryParams{
			CallbackQueryID: q.ID, Text: truncate(answer, maxPressAnswer),
		}); err != nil {
			slog.Warn("channel could not answer a press", "channel", b.name, "error", b.describe(err))
		}
	}()
	if q.From.IsBot || q.From.ID == 0 || q.Data == "" {
		return
	}
	in := comm.ChannelInbound{
		Channel: b.name, Sender: strconv.FormatInt(q.From.ID, 10),
		Text: q.Data, Raw: truncate(q.Data, 4096), Press: true,
	}
	var where *models.Chat
	switch {
	case q.Message.Message != nil:
		where = &q.Message.Message.Chat
	case q.Message.InaccessibleMessage != nil:
		where = &q.Message.InaccessibleMessage.Chat
	}
	switch {
	case where != nil:
		if q.Message.Message != nil {
			in.Pressed = native(where.ID, q.Message.Message.ID)
		}
		if where.Type != models.ChatTypePrivate {
			in.Group, in.Conversation = true, strconv.FormatInt(where.ID, 10)
		}
	default:
		// A press on no message the service names cannot be placed; it is
		// treated as a group's, which is recorded and decides nothing.
		in.Group = true
	}
	res, err := b.h.ReceiveFromChannel(ctx, in)
	switch {
	case errors.Is(err, comm.ErrRecord):
		slog.Error("channel could not record a rejected inbound", "channel", b.name, "error", err)
	case err != nil:
		answer = err.Error()
	default:
		answer = res.Reply
	}
}
