package webhook

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Event is the normalized payload the receiver publishes. Conclusion is empty
// for pull_request events because GitHub does not carry a check result there.
type Event struct {
	DeliveryID string
	Name       string
	Repo       string
	PR         int
	HeadSHA    string
	Conclusion string
}

var (
	// ErrUnsupportedEvent reports a webhook name the receiver does not handle.
	ErrUnsupportedEvent = errors.New("webhook: unsupported event")
	// ErrInvalidPayload reports a malformed or incomplete webhook body.
	ErrInvalidPayload = errors.New("webhook: invalid payload")
)

type ownerRef struct {
	FullName string `json:"full_name"`
}

type objectRef struct {
	SHA string `json:"sha"`
}

type pullRequestRef struct {
	Number int `json:"number"`
}

type checkRunEvent struct {
	Repository ownerRef `json:"repository"`
	CheckRun   struct {
		HeadSHA      string           `json:"head_sha"`
		Conclusion   string           `json:"conclusion"`
		PullRequests []pullRequestRef `json:"pull_requests"`
	} `json:"check_run"`
}

type checkSuiteEvent struct {
	Repository  ownerRef `json:"repository"`
	CheckSuite struct {
		HeadSHA      string           `json:"head_sha"`
		Conclusion   string           `json:"conclusion"`
		PullRequests []pullRequestRef `json:"pull_requests"`
	} `json:"check_suite"`
}

type pullRequestEvent struct {
	Repository  ownerRef `json:"repository"`
	Number      int      `json:"number"`
	PullRequest struct {
		Head objectRef `json:"head"`
	} `json:"pull_request"`
}

// ParseEvent narrows a raw GitHub payload into the three fields Beacon needs.
// It reads only the named event type, so a check_run payload cannot be treated
// as a pull_request payload by changing the X-GitHub-Event header alone.
func ParseEvent(name string, payload []byte) (Event, error) {
	var ev Event
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "check_run":
		var raw checkRunEvent
		if err := json.Unmarshal(payload, &raw); err != nil {
			return Event{}, fmt.Errorf("%w: check_run: %v", ErrInvalidPayload, err)
		}
		ev = Event{
			Name:       "check_run",
			Repo:       raw.Repository.FullName,
			PR:         firstPR(raw.CheckRun.PullRequests),
			HeadSHA:    raw.CheckRun.HeadSHA,
			Conclusion: raw.CheckRun.Conclusion,
		}
	case "check_suite":
		var raw checkSuiteEvent
		if err := json.Unmarshal(payload, &raw); err != nil {
			return Event{}, fmt.Errorf("%w: check_suite: %v", ErrInvalidPayload, err)
		}
		ev = Event{
			Name:       "check_suite",
			Repo:       raw.Repository.FullName,
			PR:         firstPR(raw.CheckSuite.PullRequests),
			HeadSHA:    raw.CheckSuite.HeadSHA,
			Conclusion: raw.CheckSuite.Conclusion,
		}
	case "pull_request":
		var raw pullRequestEvent
		if err := json.Unmarshal(payload, &raw); err != nil {
			return Event{}, fmt.Errorf("%w: pull_request: %v", ErrInvalidPayload, err)
		}
		ev = Event{
			Name:    "pull_request",
			Repo:    raw.Repository.FullName,
			PR:      raw.Number,
			HeadSHA: raw.PullRequest.Head.SHA,
		}
	default:
		return Event{}, fmt.Errorf("%w: %q", ErrUnsupportedEvent, name)
	}
	if err := ev.validate(); err != nil {
		return Event{}, err
	}
	return ev, nil
}

func firstPR(refs []pullRequestRef) int {
	for _, ref := range refs {
		if ref.Number > 0 {
			return ref.Number
		}
	}
	return 0
}

func (e Event) validate() error {
	if e.Repo == "" {
		return fmt.Errorf("%w: repository.full_name is empty", ErrInvalidPayload)
	}
	if e.PR <= 0 {
		return fmt.Errorf("%w: pull request number is %d", ErrInvalidPayload, e.PR)
	}
	if e.HeadSHA == "" {
		return fmt.Errorf("%w: head sha is empty", ErrInvalidPayload)
	}
	return nil
}
