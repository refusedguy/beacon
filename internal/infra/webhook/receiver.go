package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

const (
	// Path is the local endpoint GitHub sends deliveries to.
	Path = "/webhook/github"
	// DeliveryHeader carries the unique id of one delivery.
	DeliveryHeader = "X-GitHub-Delivery"
	// EventHeader names the GitHub event type.
	EventHeader = "X-GitHub-Event"

	webhookSourceID = "github-webhook"
	maxBodyBytes    = 1 << 20
)

// Config supplies the receiver's boundary dependencies. Secret and Store are
// required; the rest get local defaults.
type Config struct {
	Secret     string
	Store      domain.Store
	Clock      domain.Clock
	Deliveries DeliveryStore
	DryRun     bool
}

// Receiver handles one signed webhook endpoint. It holds the domain ports and
// the delivery ledger; it never opens a socket itself.
type Receiver struct {
	secret     string
	store      domain.Store
	clock      domain.Clock
	deliveries DeliveryStore
	dryRun     bool
	fanout     *domain.Fanout
}

// Result is the response the caller sees. Status is ok, dry_run, duplicate or
// an error string carried by errorResponse.
type Result struct {
	Status     string `json:"status"`
	DeliveryID string `json:"deliveryID"`
	Event      string `json:"event"`
	Repo       string `json:"repo"`
	PR         int    `json:"pr"`
	HeadSHA    string `json:"headSHA"`
	WatchID    string `json:"watchID,omitempty"`
	Verdict    string `json:"verdict,omitempty"`
	Revision   uint64 `json:"revision,omitempty"`
}

type errorResponse struct {
	Error string `json:"error"`
}

// New validates the config and builds a Receiver. An empty secret or a nil
// store is rejected here, at the boundary, instead of failing on first event.
func New(cfg Config) (*Receiver, error) {
	if strings.TrimSpace(cfg.Secret) == "" {
		return nil, errors.New("webhook: secret is empty")
	}
	if cfg.Store == nil {
		return nil, errors.New("webhook: store is nil")
	}
	clock := cfg.Clock
	if clock == nil {
		clock = wallClock{}
	}
	deliveries := cfg.Deliveries
	if deliveries == nil {
		deliveries = NewMemoryDeliveryStore()
	}
	return &Receiver{
		secret:     cfg.Secret,
		store:      cfg.Store,
		clock:      clock,
		deliveries: deliveries,
		dryRun:     cfg.DryRun,
		fanout:     domain.NewFanout(cfg.Store, clock),
	}, nil
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

// Handler exposes the receiver as an HTTP handler.
func (r *Receiver) Handler() http.Handler { return r }

// ServeHTTP implements http.Handler. The raw body is verified before it is
// parsed, so an unverified payload never reaches the domain layer.
func (r *Receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path != Path {
		http.NotFound(w, req)
		return
	}
	if req.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"})
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, maxBodyBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "cannot read body: " + err.Error()})
		return
	}
	if err := VerifySignature(body, req.Header.Get(SignatureHeader), r.secret); err != nil {
		writeJSON(w, http.StatusUnauthorized, errorResponse{Error: err.Error()})
		return
	}

	deliveryID := strings.TrimSpace(req.Header.Get(DeliveryHeader))
	if deliveryID == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "missing " + DeliveryHeader})
		return
	}
	ev, err := ParseEvent(req.Header.Get(EventHeader), body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	ev.DeliveryID = deliveryID

	// A dry run has no side effects, so it also leaves the delivery ledger
	// untouched. The same delivery can be previewed again after a real run.
	if !r.dryRun && !r.deliveries.Claim(deliveryID) {
		writeJSON(w, http.StatusOK, Result{
			Status: "duplicate", DeliveryID: deliveryID,
			Event: ev.Name, Repo: ev.Repo, PR: ev.PR, HeadSHA: ev.HeadSHA,
		})
		return
	}

	res, err := r.publish(ev)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// publish narrows the event to a verdict, then updates the Watch through the
// domain state machine. A dry run stops before any store write.
func (r *Receiver) publish(ev Event) (Result, error) {
	verdict := verdictFromConclusion(ev.Conclusion)
	res := Result{
		Status: "ok", DeliveryID: ev.DeliveryID, Event: ev.Name,
		Repo: ev.Repo, PR: ev.PR, HeadSHA: ev.HeadSHA, Verdict: string(verdict),
	}
	if r.dryRun {
		res.Status = "dry_run"
		return res, nil
	}

	current, err := r.ensureWatch(ev)
	if err != nil {
		return Result{}, err
	}
	next, _ := domain.Transition(current, domain.VerdictEvent(verdict, r.clock.Now()))
	res.WatchID = next.ID
	res.Revision = next.Revision
	if next.Revision == current.Revision {
		return res, nil
	}
	if err := r.store.SaveWatch(next); err != nil {
		return Result{}, err
	}
	for _, sub := range next.Subscribers {
		id := domain.ObligationKey(next.ID, next.Revision, sub.ID)
		if _, err := r.store.Obligation(id); err == nil {
			continue
		}
		o := domain.Obligation{
			ID: id, WatchID: next.ID, Revision: next.Revision,
			SubscriberID: sub.ID, VerdictPointer: verdict, StagedAt: r.clock.Now(),
		}
		if err := r.store.SaveObligation(o); err != nil {
			return Result{}, err
		}
	}
	log.Printf("webhook: %s#%d %s -> %s revision=%d delivery=%s",
		ev.Repo, ev.PR, ev.Name, verdict, next.Revision, ev.DeliveryID)
	return res, nil
}

// ensureWatch returns the active Watch for the event's PR head. A new head
// gets a Watch through Fanout; an existing one is reused so the webhook does
// not add itself as a subscriber to a real Watch.
func (r *Receiver) ensureWatch(ev Event) (domain.Watch, error) {
	active, err := r.store.ActiveWatches()
	if err != nil {
		return domain.Watch{}, err
	}
	for i := range active {
		w := active[i]
		if w.Repo == ev.Repo && w.PR == ev.PR && w.HeadSHA == ev.HeadSHA {
			return w, nil
		}
	}
	sub, err := r.fanout.Subscribe(domain.Watch{
		Repo: ev.Repo, PR: ev.PR, HeadSHA: ev.HeadSHA,
	}, domain.Subscriber{
		ID: webhookSourceID, Role: domain.RoleExecutor,
		Adapter: "github-webhook", Target: ev.Repo,
	})
	if err != nil {
		return domain.Watch{}, err
	}
	return sub.Watch, nil
}

// verdictFromConclusion maps a GitHub check conclusion to a Beacon verdict.
// An empty or neutral conclusion leaves the Watch watching, so a webhook
// without a result cannot finish a task.
func verdictFromConclusion(conclusion string) domain.Verdict {
	switch strings.ToLower(strings.TrimSpace(conclusion)) {
	case "success":
		return domain.VerdictReady
	case "failure", "timed_out", "cancelled", "startup_failure", "action_required", "stale":
		return domain.VerdictRetry
	default:
		return domain.VerdictWatching
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// Serve runs h on addr until ctx is cancelled, then shuts the server down.
// The context keeps socket handling inside infra; cmd only wires it.
func Serve(ctx context.Context, addr string, h http.Handler) error {
	if h == nil {
		return errors.New("webhook: nil handler")
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("webhook: serve %s: %w", addr, err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("webhook: shutdown: %w", err)
		}
		<-errCh
		return nil
	}
}
