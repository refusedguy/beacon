package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/channel"
	"github.com/refusedguy/beacon/internal/infra/httpapi"
	"github.com/refusedguy/beacon/internal/infra/observe"
	"github.com/refusedguy/beacon/internal/infra/store"
	"github.com/refusedguy/beacon/internal/infra/wake"
)

func init() {
	catalog = append(catalog, command{
		Name:    "up",
		Summary: "Автономный цикл Beacon: ReconcileLoop, статус и команды Telegram",
		Usage:   "beacon up [--interval 2s] [--once]",
	})
	sort.Slice(catalog, func(i, j int) bool { return catalog[i].Name < catalog[j].Name })
}

// upRunner is the dispatch seam, like the other command runners. RegisterUp
// replaces it in tests.
var upRunner = func(c *CLI, args []string) int { return runUp(c, args) }

// RegisterUp installs the up command implementation.
func RegisterUp(fn func(*CLI, []string) int) { upRunner = fn }

// upStatusPort is the read-only slice of the store the status and inspect
// replies need. A *store.DB satisfies it; tests pass a fake.
type upStatusPort interface {
	LiveWatches() ([]domain.Watch, error)
	ActiveBlockers() ([]domain.BlockerIncident, error)
}

// upDecisionPort is the existing human-decide port. The cmd layer never opens a
// socket: the HTTP client and the SQLite adapter live behind this interface.
type upDecisionPort interface {
	Decide(context.Context, httpapi.ClientDecision) (httpapi.ClientDecisionReceipt, error)
}

// upMessenger is the reply side of Telegram. Keeping it narrow lets a command
// test run with no token and no network.
type upMessenger interface {
	SendText(ctx context.Context, chatID, text string) error
}

var (
	upDecisionsFor = newUpDecisionPort
	upTelegramFor  = func() *wake.TelegramNotifySink {
		return wake.NewTelegramNotifySink(wake.TelegramConfigFromEnv(nil))
	}
	upNtfyFor = func() *wake.NtfyNotifySink {
		return wake.NewNtfyNotifySink(wake.NtfyConfigFromEnv(nil))
	}
)

// channelMessagePort is the narrow view of the channel ingress the loop uses.
// *channel.Ingress satisfies it; tests install a fake to exercise the route.
type channelMessagePort interface {
	Accept(ctx context.Context, msg channel.Message) (channel.Command, error)
}

// upChannelIngress is nil until the up command opts in. The loop installs the
// boundary before it starts ticking and clears it on exit, so a plain chat
// message keeps the old behaviour on a server without channels enabled.
var upChannelIngress channelMessagePort

// upActorResolver authorizes slash commands. runUp installs the same resolver
// the channel ingress uses, so a chat sender must be in the known-sender
// registry before a slash command reaches the decision port. It stays nil only
// when the handler runs outside runUp, such as in a direct handler test.
var upActorResolver channel.ActorResolver

// upDecisionAdapter prefers the HTTP human-decide port and falls back to the
// local SQLite port. The fallback also covers a server that answers with a
// protocol error, so a long-running `up` session keeps accepting commands.
type upDecisionAdapter struct {
	decider humanDecider
	store   humanDecisionStore
}

func (a upDecisionAdapter) Decide(ctx context.Context, req httpapi.ClientDecision) (httpapi.ClientDecisionReceipt, error) {
	if a.decider != nil {
		if receipt, err := a.decider.Decide(ctx, req); err == nil {
			return receipt, nil
		}
	}
	if a.store == nil {
		return httpapi.ClientDecisionReceipt{}, fmt.Errorf("beacon up: human-decide port не подключён")
	}
	if err := a.store.Apply(ctx, req); err != nil {
		return httpapi.ClientDecisionReceipt{}, err
	}
	return httpapi.ClientDecisionReceipt{OK: true, Applied: true}, nil
}

// newUpDecisionPort builds the decision port from the same connection file and
// state directory the human command uses. Both concrete ports are adapted from
// code already in this package; no transport is opened here.
func newUpDecisionPort(dir string) upDecisionPort {
	if conn, err := httpapi.LoadClientConn(dir); err == nil {
		if decider, err := humanDeciderFor(conn); err == nil {
			return upDecisionAdapter{decider: decider, store: humanStoreFor(dir)}
		}
	}
	return upDecisionAdapter{store: humanStoreFor(dir)}
}

// installUpNotifications tees the three human-attention observe events to every
// configured notification sink, Telegram and ntfy. The typed Recorder variant
// keeps histogram support; the generic variant covers a test sink or the no-op
// default.
func installUpNotifications(sink wake.NotificationSink) func() {
	previous := observe.Default()
	var next observe.Sink
	switch p := previous.(type) {
	case *observe.Recorder:
		next = &wake.RecorderEventNotifySink{Recorder: p, Notify: sink}
	default:
		next = wake.NewEventNotifySink(previous, sink)
	}
	observe.SetDefault(next)
	return func() { observe.SetDefault(previous) }
}

// runUp implements `beacon up [--interval 2s] [--once]`. It starts the same
// single ReconcileLoop as `beacon mcp` and `beacon daemon`, prints a compact
// status on every tick and serves bot commands from Telegram. There is no
// OpenCode session in the loop: the terminal process is the whole runtime.
func runUp(c *CLI, args []string) int {
	channelsCfg, err := channelConfigFromEnv(nil)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon up: channel config: %v\n", err)
		return 2
	}

	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	fs.SetOutput(c.errOut)
	fs.Usage = func() {}
	interval := fs.Duration("interval", 2*time.Second, "интервал опроса Telegram и вывода статуса")
	once := fs.Bool("once", false, "один проход ReconcilePass и выход")
	channels := fs.Bool("channels", channelsCfg.Enabled, "принимать команды из внешнего канала")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		writeUsage(c.errOut)
		return 2
	}
	if *interval <= 0 {
		fmt.Fprintf(c.errOut, "beacon up: интервал должен быть положительным\n")
		return 2
	}
	channelsCfg.Enabled = *channels

	// Slash commands skip the channel ingress, so authorize their sender with
	// the same registry. An empty registry refuses every slash command.
	upActorResolver = channelSenderResolver{senders: channelsCfg.Senders}
	defer func() { upActorResolver = nil }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dir, err := stateDir()
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon up: %v\n", err)
		return 2
	}
	db, err := openProcessStore(dir)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon up: %v\n", err)
		return 2
	}
	defer func() { _ = db.Close() }()

	ghBatch := newGitHubBatch(ctx, c)
	deps := buildMCPDependencies(db, dir, ghBatch.provider.PointStats)
	cfg := reconcileConfig{
		dir:        dir,
		db:         db,
		deps:       deps,
		batch:      ghBatch.provider,
		counter:    ghBatch.counter,
		projectKey: orchestratorProjectKey(),
		root:       projectRootForConfig(),
	}

	tg := upTelegramFor()
	ntfy := upNtfyFor()
	restore := installUpNotifications(wake.NewMultiNotificationSink(tg, ntfy))
	defer restore()
	decisions := upDecisionsFor(dir)
	status := upStatusPort(db)

	if channelsCfg.Enabled {
		ingress, err := buildChannelIngress(channelsCfg, store.NewOutboxStore(db), channelApplySink(decisions))
		if err != nil {
			fmt.Fprintf(c.errOut, "beacon up: channel ingress: %v\n", err)
			return 2
		}
		upChannelIngress = ingress
		defer func() { upChannelIngress = nil }()
	}

	stopLoop, err := runReconciler(ctx, cfg)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon up: poller: %v\n", err)
		return 2
	}
	defer stopLoop()

	if *once {
		ReconcilePass(ctx, cfg)
		fmt.Fprint(c.out, renderUpStatus(ctx, status))
		return 0
	}

	fmt.Fprintf(c.out, "beacon up: telegram=%s ntfy=%s channels=%s interval=%s\n",
		upNotifyState(tg), upNtfyState(ntfy), upChannelState(), *interval)
	fmt.Fprint(c.out, renderUpStatus(ctx, status))

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	var offset int64
	for {
		select {
		case <-ctx.Done():
			return 0
		case <-ticker.C:
			upHandleTick(ctx, c, tg, status, decisions, &offset)
		}
	}
}

func upNotifyState(tg *wake.TelegramNotifySink) string {
	if tg != nil && tg.Configured() {
		return "on"
	}
	return "off"
}

func upNtfyState(ntfy *wake.NtfyNotifySink) string {
	if ntfy != nil && ntfy.Configured() {
		return "on"
	}
	return "off"
}

func upChannelState() string {
	if upChannelIngress != nil {
		return "on"
	}
	return "off"
}

// upHandleTick drains the command queue once and prints the compact status.
// The offset advances past every update, including ones that are not commands,
// so a noisy chat cannot trap the loop on the same message.
func upHandleTick(ctx context.Context, c *CLI, tg *wake.TelegramNotifySink, status upStatusPort, decisions upDecisionPort, offset *int64) {
	updates, err := tg.Updates(ctx, *offset)
	if err != nil {
		observe.ActorError("telegram-updates", "", err)
		return
	}
	for _, update := range updates {
		if update.UpdateID >= *offset {
			*offset = update.UpdateID + 1
		}
		handleUpCommand(ctx, tg, update.Message.Chat.ID.String(), update.Message.Text, status, decisions, c.out)
	}
	fmt.Fprint(c.out, renderUpStatus(ctx, status))
}

// authorizeUpSlashSender resolves a Telegram sender through the same
// ActorResolver the channel ingress uses. A slash command never reaches the
// decision port without a known actor. A nil resolver leaves the handler's
// legacy behaviour intact for callers outside runUp.
func authorizeUpSlashSender(ctx context.Context, sender string) (domain.ActorID, error) {
	resolver := upActorResolver
	if resolver == nil {
		return "", nil
	}
	actor, err := resolver.Resolve(ctx, channelTelegram, sender)
	if err != nil {
		return "", fmt.Errorf("beacon up: slash sender %q: %w", sender, err)
	}
	return actor, nil
}

// handleUpCommand parses one chat message and answers through the messenger.
// Read-only commands render the store projection; write commands go through the
// human-decide port so the same idempotency and journal rules apply as to
// `beacon human`. With channels enabled, a plain-text command first goes
// through the channel ingress; a slash command keeps the existing route.
func handleUpCommand(ctx context.Context, messenger upMessenger, chatID, text string, status upStatusPort, decisions upDecisionPort, out io.Writer) {
	if ingress := upChannelIngress; ingress != nil && !strings.HasPrefix(strings.TrimSpace(text), "/") {
		if acceptUpChannelMessage(ctx, messenger, chatID, text, ingress, out) {
			return
		}
	}
	cmd, ok := wake.ParseBotCommand(text)
	if !ok {
		return
	}
	actor, err := authorizeUpSlashSender(ctx, chatID)
	if err != nil {
		observe.ActorError("telegram-slash", chatID, err)
		if messenger != nil {
			_ = messenger.SendText(ctx, chatID, "slash: sender не авторизован")
		}
		if out != nil {
			fmt.Fprintf(out, "beacon up: slash refusal from %s: %v\n", chatID, err)
		}
		return
	}
	var reply string
	switch cmd.Command {
	case wake.BotStatus:
		reply = strings.TrimSpace(renderUpStatus(ctx, status))
	case wake.BotInspect:
		reply = strings.TrimSpace(renderInspectText(ctx, status, cmd.Target))
		if reply == "" {
			reply = "inspect: ничего не найдено"
		}
	case wake.BotApprove, wake.BotUnblock, wake.BotPause:
		if cmd.Target == "" {
			reply = string(cmd.Command) + ": нужен watchID или blockerID"
			break
		}
		verb := strings.TrimPrefix(string(cmd.Command), "/")
		receipt, err := decisions.Decide(ctx, httpapi.ClientDecision{Verb: verb, Target: cmd.Target, Note: cmd.Note, Actor: string(actor)})
		if err != nil {
			reply = fmt.Sprintf("%s %s: %v", verb, cmd.Target, err)
			break
		}
		reply = fmt.Sprintf("%s %s: ok applied=%t", verb, cmd.Target, receipt.Applied)
		if receipt.Message != "" {
			reply += " " + receipt.Message
		}
	default:
		return
	}
	if reply == "" || messenger == nil {
		return
	}
	if err := messenger.SendText(ctx, chatID, reply); err != nil {
		observe.ActorError("telegram-reply", "", err)
		return
	}
	if out != nil {
		fmt.Fprintf(out, "beacon up: %s -> %s\n", cmd.Command, chatID)
	}
}

// acceptUpChannelMessage sends one plain chat message through the channel
// boundary. It reports true only when the ingress delivered a command, so a
// message the boundary refuses falls through to the slash-command handler.
func acceptUpChannelMessage(ctx context.Context, messenger upMessenger, chatID, text string, ingress channelMessagePort, out io.Writer) bool {
	cmd, err := ingress.Accept(ctx, channel.Message{
		Channel: channelTelegram,
		Sender:  chatID,
		Text:    text,
	})
	if err != nil {
		return false
	}
	reply := fmt.Sprintf("%s %s: ok", cmd.Verb, cmd.Target)
	if messenger != nil {
		if err := messenger.SendText(ctx, chatID, reply); err != nil {
			observe.ActorError("channel-reply", string(cmd.Actor), err)
		}
	}
	if out != nil {
		fmt.Fprintf(out, "beacon up: channel %s -> %s\n", cmd.Verb, chatID)
	}
	return true
}

// renderUpStatus is the compact terminal line printed each tick and the reply
// to /status. It reports counts, not every Watch body, so a busy project stays
// readable.
func renderUpStatus(ctx context.Context, st upStatusPort) string {
	watches, err := st.LiveWatches()
	if err != nil {
		return fmt.Sprintf("beacon up: watches unavailable: %v\n", err)
	}
	blockers, err := st.ActiveBlockers()
	if err != nil {
		return fmt.Sprintf("beacon up: blockers unavailable: %v\n", err)
	}
	counts := make(map[string]int)
	attention := 0
	for _, w := range watches {
		verdict := string(w.Verdict)
		if verdict == "" {
			verdict = "-"
		}
		counts[verdict]++
		if upNeedsAttention(w) {
			attention++
		}
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, counts[key]))
	}
	return fmt.Sprintf("beacon up: watches=%d blockers=%d attention=%d verdicts=[%s]\n",
		len(watches), len(blockers), attention, strings.Join(parts, ","))
}

// upNeedsAttention reports whether a Watch is blocked or has an orphaned
// subscriber. Those are the two states a human can unblock from Telegram.
func upNeedsAttention(w domain.Watch) bool {
	if w.Blocked != nil {
		return true
	}
	for _, sub := range w.Subscribers {
		if sub.Phase == domain.WorkerPhaseOrphaned {
			return true
		}
	}
	return false
}

// renderInspectText is the reply to /inspect. An empty query lists every live
// Watch; a PR number or a watchID substring narrows the result.
func renderInspectText(ctx context.Context, st upStatusPort, query string) string {
	watches, err := st.LiveWatches()
	if err != nil {
		return "inspect: " + err.Error()
	}
	var b strings.Builder
	found := 0
	for _, w := range watches {
		if !upInspectMatches(query, w) {
			continue
		}
		found++
		fmt.Fprintf(&b, "watch=%s pr=%d status=%s verdict=%s revision=%d\n",
			w.ID, w.PR, w.Status, w.Verdict, w.Revision)
		if w.Blocked != nil {
			fmt.Fprintf(&b, "  blocked reason=%s checks=[%s] fixAttempts=%d\n",
				w.Blocked.Reason, strings.Join(w.Blocked.FailedChecks, ","), w.Blocked.FixAttempts)
		}
		for _, sub := range w.Subscribers {
			fmt.Fprintf(&b, "  subscriber=%s role=%s phase=%s\n", sub.ID, sub.Role, sub.Phase)
		}
	}
	if found == 0 {
		return ""
	}
	return b.String()
}

func upInspectMatches(query string, w domain.Watch) bool {
	query = strings.TrimSpace(query)
	if query == "" {
		return true
	}
	if pr, err := strconv.Atoi(query); err == nil && w.PR == pr {
		return true
	}
	return strings.Contains(w.ID, query)
}
