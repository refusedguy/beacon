package taskcli

// fixChecksRepo owns fix-checks.json and its private flock.
type fixChecksRepo struct {
	fileState[fixChecksState]
}

func newFixChecksRepo(cfg Config) fixChecksRepo {
	return fixChecksRepo{fileState[fixChecksState]{
		stateDir:    cfg.StateDir,
		root:        cfg.Root,
		file:        "fix-checks.json",
		lockFile:    "fix-checks.lock",
		readErr:     "не прочитано состояние fix-checks: %v",
		parseErr:    "не разобрано состояние fix-checks: %v",
		writeErr:    "не собрано состояние fix-checks: %v",
		lockOpenErr: "не открыт замок fix-checks: %v",
		lockTakeErr: "не взят замок fix-checks: %v",
	}}
}
