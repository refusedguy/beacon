package taskcli

// reservationRepo owns file-reservations.json and its private flock.
type reservationRepo struct {
	fileState[fileReservationState]
}

func newReservationRepo(cfg Config) reservationRepo {
	return reservationRepo{fileState[fileReservationState]{
		stateDir:    cfg.StateDir,
		root:        cfg.Root,
		file:        "file-reservations.json",
		lockFile:    "file-reservations.lock",
		readErr:     "не прочитана аренда файлов: %v",
		parseErr:    "не разобрана аренда файлов: %v",
		writeErr:    "не собрана аренда файлов: %v",
		lockOpenErr: "не открыт замок аренды: %v",
		lockTakeErr: "не взят замок аренды: %v",
	}}
}
