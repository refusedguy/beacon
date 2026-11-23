package main

import (
	"bytes"
	"testing"
)

func testRoot(t *testing.T) string {
	t.Helper()
	root, err := findRoot(".")
	if err != nil {
		t.Fatalf("find repository root: %v", err)
	}
	return root
}

// TestCandidateFixtureMatchesSource proves the candidate is derived from the
// descriptor table and that the committed fixture is current.
func TestCandidateFixtureMatchesSource(t *testing.T) {
	root := testRoot(t)
	descriptors, err := loadDescriptors(root)
	if err != nil {
		t.Fatalf("load descriptors: %v", err)
	}
	if len(descriptors) != 11 {
		t.Fatalf("descriptor count = %d, want 11", len(descriptors))
	}

	candidate := candidateText(descriptors)
	committed, err := readFixture(root)
	if err != nil {
		t.Fatalf("read candidate fixture: %v", err)
	}
	if err := compare(candidate, committed); err != nil {
		t.Fatalf("candidate does not match committed fixture: %v", err)
	}
	size, sha := fingerprint(candidate)
	if size == 0 || sha == "" {
		t.Fatal("empty fingerprint")
	}
}

// TestPlantedDescriptionDiscrepancyFailsCheck is the negative control. One
// changed description must move the fingerprint and fail the committed-fixture
// comparison. A check that always passes would keep this test green.
func TestPlantedDescriptionDiscrepancyFailsCheck(t *testing.T) {
	root := testRoot(t)
	descriptors, err := loadDescriptors(root)
	if err != nil {
		t.Fatalf("load descriptors: %v", err)
	}
	base := candidateText(descriptors)
	planted := plantedCandidate(descriptors)

	if bytes.Equal(base, planted) {
		t.Fatal("planted description did not change the candidate")
	}
	baseSize, baseSHA := fingerprint(base)
	plantedSize, plantedSHA := fingerprint(planted)
	if baseSize == plantedSize || baseSHA == plantedSHA {
		t.Fatalf("fingerprint did not move: base %d/%s, planted %d/%s", baseSize, baseSHA, plantedSize, plantedSHA)
	}
	if err := compare(planted, base); err == nil {
		t.Fatal("planted candidate passed the committed-fixture check")
	}

	committed, err := readFixture(root)
	if err != nil {
		t.Fatalf("read candidate fixture: %v", err)
	}
	if err := compare(planted, committed); err == nil {
		t.Fatal("planted candidate passed against the real fixture")
	}
}

// TestSelfTestRunner mirrors the CLI self-test used in CI.
func TestSelfTestRunner(t *testing.T) {
	if err := selfTest(testRoot(t)); err != nil {
		t.Fatalf("self-test runner: %v", err)
	}
}
