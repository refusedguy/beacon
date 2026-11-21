package cibridge

// CIRefresh is the read side of one refresh. It lists the live Watches and
// normalizes raw CI responses into the observations the verdict engine reads.
type CIRefresh = ciRefresh

// VerdictApplier maps an EvaluatePR result to its follow-up action: publish a
// verdict, merge, block, close a Watch, or resolve a blocker.
type VerdictApplier = verdictApplier

// WakeDispatcher owns delivery: obligation staging, the durable wake cap, the
// per-adapter circuit breaker, escalation and the in_work stall watchdog.
type WakeDispatcher = wakeDispatcher

// ReadAPI is the read and confirm surface the orchestrator uses to inspect a
// Watch verdict and prove that the pointer was actually read.
type ReadAPI = readAPI

// Each part embeds *Bridge so its methods read the shared configuration through
// promotion. Bridge embeds the four parts so existing *Bridge call sites keep
// resolving to the same method signatures. No public method signature changed.
type ciRefresh struct{ *Bridge }
type verdictApplier struct{ *Bridge }
type wakeDispatcher struct{ *Bridge }
type readAPI struct{ *Bridge }
