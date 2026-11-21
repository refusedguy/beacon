package taskcli

// precheckRepo owns precheck.json and its private flock.
type precheckRepo struct {
	fileState[precheckState]
}

func newPrecheckRepo(cfg Config) precheckRepo {
	return precheckRepo{fileState[precheckState]{
		stateDir:    cfg.StateDir,
		root:        cfg.Root,
		file:        "precheck.json",
		lockFile:    "precheck.lock",
		readErr:     "не прочитан журнал precheck: %v",
		parseErr:    "не разобран журнал precheck: %v",
		writeErr:    "не собран журнал precheck: %v",
		lockOpenErr: "не открыт замок precheck: %v",
		lockTakeErr: "не взят замок precheck: %v",
	}}
}
