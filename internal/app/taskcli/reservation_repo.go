package taskcli

// reservationRepo owns file-reservations.json and its private flock.
type reservationRepo struct {
	fileState[fileReservationState]
}

func newReservationRepo(cfg Config) reservationRepo {
	return reservationRepo{newFileRepo[fileReservationState](cfg, fileRepoSpecs.reservation)}
}
