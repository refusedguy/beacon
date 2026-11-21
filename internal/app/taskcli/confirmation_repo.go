package taskcli

// confirmationRepo owns handoff-confirmations.json and its private flock.
type confirmationRepo struct {
	fileState[handoffConfirmationState]
}

func newConfirmationRepo(cfg Config) confirmationRepo {
	return confirmationRepo{fileState[handoffConfirmationState]{
		stateDir:    cfg.StateDir,
		root:        cfg.Root,
		file:        "handoff-confirmations.json",
		lockFile:    "handoff-confirmations.lock",
		readErr:     "не прочитаны подтверждения handoff: %v",
		parseErr:    "не разобраны подтверждения handoff: %v",
		writeErr:    "не собраны подтверждения handoff: %v",
		lockOpenErr: "не открыт замок подтверждений handoff: %v",
		lockTakeErr: "не взят замок подтверждений handoff: %v",
	}}
}
