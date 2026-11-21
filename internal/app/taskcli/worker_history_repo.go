package taskcli

// workerHistoryRepo owns worker-history.json. It uses the shared card lock,
// the same one RecordWorkerOutcome acquired before the split.
type workerHistoryRepo struct {
	fileState[WorkerHistory]
}

func newWorkerHistoryRepo(cfg Config) workerHistoryRepo {
	return workerHistoryRepo{fileState[WorkerHistory]{
		stateDir: cfg.StateDir,
		root:     cfg.Root,
		file:     "worker-history.json",
		readErr:  "не прочитана история воркеров: %v",
		parseErr: "не разобрана история воркеров: %v",
		writeErr: "не собрана история воркеров: %v",
		empty:    func() WorkerHistory { return WorkerHistory{} },
		normalize: func(h WorkerHistory) WorkerHistory {
			if h == nil {
				return WorkerHistory{}
			}
			return h
		},
	}}
}
