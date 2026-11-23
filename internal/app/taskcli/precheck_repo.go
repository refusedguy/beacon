package taskcli

// precheckRepo owns precheck.json and its private flock.
type precheckRepo struct {
	fileState[precheckState]
}

func newPrecheckRepo(cfg Config) precheckRepo {
	return precheckRepo{newFileRepo[precheckState](cfg, fileRepoSpecs.precheck)}
}
