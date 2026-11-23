package taskcli

// fixChecksRepo owns fix-checks.json and its private flock.
type fixChecksRepo struct {
	fileState[fixChecksState]
}

func newFixChecksRepo(cfg Config) fixChecksRepo {
	return fixChecksRepo{newFileRepo[fixChecksState](cfg, fileRepoSpecs.fixChecks)}
}
