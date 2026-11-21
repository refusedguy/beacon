package main

import (
	"testing"

	"github.com/refusedguy/beacon/internal/infra/wake"
)

const w381WatchID = "watch:refusedguy/beacon:360:c35188e350120913bc3b4d7b05f1aace2c22142d"

// The built-in adapter refuses a pointer whose target and obligation name
// different subscribers, so a substituted session cannot be prompted.
func TestW381DaemonAdapterRefusesForeignObligation(t *testing.T) {
	a := &daemonWakeAdapter{api: &wake.T202API{}}
	p := wake.T210WakePointer{
		Adapter:      wake.AdapterOpenCode,
		Target:       "ses_e08f34cffffeQD2vSh7CXI3gqU",
		WatchID:      w381WatchID,
		ObligationID: "wrong:1:ses_other",
		Revision:     1,
	}
	if _, ok := a.Bind(p, nil); ok {
		t.Fatal("adapter bound a target that does not own the obligation")
	}
}
