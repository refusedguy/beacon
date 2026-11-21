package taskcli

// handoffJournalRepo owns handoff-journal.json and its private flock.
type handoffJournalRepo struct {
	fileState[handoffJournalState]
}

func newHandoffJournalRepo(cfg Config) handoffJournalRepo {
	return handoffJournalRepo{fileState[handoffJournalState]{
		stateDir:    cfg.StateDir,
		root:        cfg.Root,
		file:        "handoff-journal.json",
		lockFile:    "handoff-journal.lock",
		readErr:     "не прочитан журнал handoff: %v",
		parseErr:    "не разобран журнал handoff: %v",
		writeErr:    "не собран журнал handoff: %v",
		lockOpenErr: "не открыт замок журнала handoff: %v",
		lockTakeErr: "не взят замок журнала handoff: %v",
	}}
}
