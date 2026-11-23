package cibridge

// Each part embeds *Bridge so its methods read the shared configuration through
// promotion. Bridge embeds the four parts so existing *Bridge call sites keep
// resolving to the same method signatures. No public method signature changed.
type ciRefresh struct{ *Bridge }
type verdictApplier struct{ *Bridge }
type wakeDispatcher struct{ *Bridge }
type readAPI struct{ *Bridge }
