package taskcli

// handoffJournalRepo owns handoff-journal.json and its private flock.
type handoffJournalRepo struct {
	fileState[handoffJournalState]
}

func newHandoffJournalRepo(cfg Config) handoffJournalRepo {
	return handoffJournalRepo{newFileRepo[handoffJournalState](cfg, fileRepoSpecs.handoffJournal)}
}
