package taskcli

// confirmationRepo owns handoff-confirmations.json and its private flock.
type confirmationRepo struct {
	fileState[handoffConfirmationState]
}

func newConfirmationRepo(cfg Config) confirmationRepo {
	return confirmationRepo{newFileRepo[handoffConfirmationState](cfg, fileRepoSpecs.confirmation)}
}
