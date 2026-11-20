package channel

import "testing"

// TestPlanRoutesToUser proves a direct message produces one reply back to the
// user, with the user target parameter set.
func TestPlanRoutesToUser(t *testing.T) {
	out := Plan(DefaultConfig(), Turn{
		Message: TurnMessage{Kind: TargetUser, Target: "U-42"},
	})
	if !out.Send {
		t.Fatalf("direct message must produce a reply, got %+v", out)
	}
	if out.Route.Kind != TargetUser {
		t.Fatalf("route kind = %q, want %q", out.Route.Kind, TargetUser)
	}
	if out.Route.Target != "U-42" {
		t.Fatalf("route target = %q, want %q", out.Route.Target, "U-42")
	}
	if out.Reason != ReasonReply {
		t.Fatalf("reason = %q, want %q", out.Reason, ReasonReply)
	}
}

// TestPlanRoutesToGroup proves a group message produces one reply back to the
// group, with the group target parameter set.
func TestPlanRoutesToGroup(t *testing.T) {
	out := Plan(DefaultConfig(), Turn{
		Message: TurnMessage{Kind: TargetGroup, Target: "G-7"},
	})
	if !out.Send {
		t.Fatalf("group message must produce a reply, got %+v", out)
	}
	if out.Route.Kind != TargetGroup {
		t.Fatalf("route kind = %q, want %q", out.Route.Kind, TargetGroup)
	}
	if out.Route.Target != "G-7" {
		t.Fatalf("route target = %q, want %q", out.Route.Target, "G-7")
	}
	if out.Reason != ReasonReply {
		t.Fatalf("reason = %q, want %q", out.Reason, ReasonReply)
	}
}

// TestPlanSuppressesWhenModelAlreadyReplied proves the automatic reply is
// dropped once the model called the reply tool during its turn.
func TestPlanSuppressesWhenModelAlreadyReplied(t *testing.T) {
	out := Plan(DefaultConfig(), Turn{
		Message:     TurnMessage{Kind: TargetGroup, Target: "G-7"},
		ToolsCalled: []string{"search", ReplyTool, "search"},
	})
	if out.Send {
		t.Fatalf("a model-authored reply must suppress the automatic reply, got %+v", out)
	}
	if out.Reason != ReasonSuppressed {
		t.Fatalf("reason = %q, want %q", out.Reason, ReasonSuppressed)
	}
}

// TestPlanUsesTargetParam proves the reply target_param is carried through
// unchanged and an unrelated tool call does not block the reply.
func TestPlanUsesTargetParam(t *testing.T) {
	const target = "user:ops@example.test"
	out := Plan(Config{SuppressTools: []string{ReplyTool}}, Turn{
		Message:     TurnMessage{Kind: TargetUser, Target: target},
		ToolsCalled: []string{"search"},
	})
	if !out.Send {
		t.Fatalf("non-reply tool must not suppress the reply, got %+v", out)
	}
	if out.Route.Kind != TargetUser || out.Route.Target != target {
		t.Fatalf("route = %+v, want kind %q and target %q", out.Route, TargetUser, target)
	}
}

// TestPlanEmptyState proves a turn with no message and no tools produces no
// reply and no route.
func TestPlanEmptyState(t *testing.T) {
	out := Plan(Config{}, Turn{})
	if out.Send {
		t.Fatalf("empty turn must not produce a reply, got %+v", out)
	}
	if out.Route != (Route{}) {
		t.Fatalf("empty turn must not produce a route, got %+v", out.Route)
	}
	if out.Reason != ReasonNoTarget {
		t.Fatalf("reason = %q, want %q", out.Reason, ReasonNoTarget)
	}
}

// TestPlanRejectsInvalidTarget proves a missing identifier or an unknown route
// kind cannot produce a reply. Both are empty-state variants at the route
// boundary.
func TestPlanRejectsInvalidTarget(t *testing.T) {
	cases := []struct {
		name    string
		message TurnMessage
	}{
		{"empty target", TurnMessage{Kind: TargetUser}},
		{"unknown kind", TurnMessage{Kind: TargetKind("email"), Target: "a@b.test"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := Plan(DefaultConfig(), Turn{Message: tc.message})
			if out.Send {
				t.Fatalf("invalid target must not produce a reply, got %+v", out)
			}
			if out.Reason != ReasonNoTarget {
				t.Fatalf("reason = %q, want %q", out.Reason, ReasonNoTarget)
			}
		})
	}
}

// TestPlanSuppressionIsOrderIndependent proves suppression depends on set
// membership, not on the order of tool calls.
func TestPlanSuppressionIsOrderIndependent(t *testing.T) {
	turn := Turn{
		Message:     TurnMessage{Kind: TargetUser, Target: "U-1"},
		ToolsCalled: []string{ReplyTool, "search"},
	}
	first := Plan(DefaultConfig(), turn)
	turn.ToolsCalled = []string{"search", ReplyTool}
	second := Plan(DefaultConfig(), turn)
	if first != second {
		t.Fatalf("tool call order changed the outcome: %+v vs %+v", first, second)
	}
	if first.Send || first.Reason != ReasonSuppressed {
		t.Fatalf("reply tool must suppress regardless of order, got %+v", first)
	}
}
