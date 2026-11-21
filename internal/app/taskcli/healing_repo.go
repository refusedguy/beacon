package taskcli

// healingRepo owns ci-healing.json and its private flock. A null JSON value
// still gets an initialized attempts map, as before the split.
type healingRepo struct {
	fileState[ciHealingState]
}

func newHealingRepo(cfg Config) healingRepo {
	return healingRepo{fileState[ciHealingState]{
		stateDir:    cfg.StateDir,
		root:        cfg.Root,
		file:        "ci-healing.json",
		lockFile:    "ci-healing.lock",
		readErr:     "не прочитано состояние healing: %v",
		parseErr:    "не разобрано состояние healing: %v",
		writeErr:    "не собрано состояние healing: %v",
		lockOpenErr: "не открыт замок healing: %v",
		lockTakeErr: "не взят замок healing: %v",
		empty:       func() ciHealingState { return ciHealingState{Attempts: map[string]int{}} },
		normalize: func(st ciHealingState) ciHealingState {
			if st.Attempts == nil {
				st.Attempts = map[string]int{}
			}
			return st
		},
	}}
}
