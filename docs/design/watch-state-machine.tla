---- MODULE watch_state_machine ----
\* PSK-03. Formal model of the Beacon Watch lifecycle.
\*
\* This module is the authoritative statement of the three Watch safety rules
\* that PSK-03 asks to verify (see docs/design/watch-state-machine.md):
\*
\*   1. NoDoubleMerge       - one Watch is merged at most once.
\*   2. VerdictNeedsRevision - a verdict never changes without a new revision.
\*   3. BlockClearedOnlyByResolved - a block lifts only after blocked:resolved.
\*
\* The transition relation mirrors internal/domain/transition.go (Transition,
\* transitionVerdict, blockWatch, unblockWatch) and the apply paths in
\* internal/infra/cibridge/verdict_applier.go (applyMerge, applyMerged,
\* closeWatch). The model lives outside the Go binary and adds no dependency
\* to go.mod.

EXTENDS Naturals, FiniteSets

\* ---------------------------------------------------------------------------
\* Guard toggles (the control patches).
\*
\* Each of these is a single-line definition on purpose. scripts/verify-watch-
\* model.sh parses these exact lines for its offline explorer, and TLC reads
\* them as ordinary operators. Changing TRUE to FALSE removes that guard, so
\* the corresponding invariant becomes reachable and TLC prints the shortest
\* counterexample. Restore TRUE to make the model green again.
\* ---------------------------------------------------------------------------
MERGE_ONCE == TRUE
VERDICT_REVISION == TRUE
BLOCK_RESOLVED_ONLY == TRUE

\* ---------------------------------------------------------------------------
\* Bounded parameters. The bounds keep the state graph finite for exhaustive
\* checking; they are model boundaries, not product limits.
\* ---------------------------------------------------------------------------
Watches == {"w:psk03:a", "w:psk03:b"}
Subscribers == {"executor", "orchestrator", "fixer"}
Verdicts == {
    "watching", "ready", "rebase", "retry", "keep_working", "timeout",
    "merged", "closed", "blocked", "recheck"
    }
TerminalVerdicts == {"merged", "closed"}
LiveStatuses == {"watching", "notifying"}
MaxRevision == 6
Revisions == 0..MaxRevision

\* ---------------------------------------------------------------------------
\* Variables.
\*   watch            - the Watch identifier (ObligationKey prefix).
\*   status           - watching / notifying / blocked / closed.
\*   revision         - grows only when the verdict changes (ADR: Transition).
\*   verdict          - result computed for one revision.
\*   subscriber       - one subscriber whose obligation is staged on this rev.
\*   mergeLedger      - successful merge count per Watch; the "already merged"
\*                      check is mergeLedger[watch] = 0, matching closeWatch's
\*                      "next.Revision = w.Revision -> return 0" guard.
\*   lastFrom* / lastTo* - the most recent verdict/revision pair, so
\*                      VerdictNeedsRevision is a state invariant rather than
\*                      a property over a trace.
\*   blockClearReason - the decision that last lifted a block.
\* ---------------------------------------------------------------------------
VARIABLES
    watch, status, revision, verdict, subscriber, mergeLedger,
    lastFromVerdict, lastFromRevision, lastToVerdict, lastToRevision,
    blockClearReason

vars == <<watch, status, revision, verdict, subscriber, mergeLedger,
          lastFromVerdict, lastFromRevision, lastToVerdict, lastToRevision,
          blockClearReason>>

Init ==
    /\ watch = "w:psk03:a"
    /\ status = "watching"
    /\ revision = 0
    /\ verdict = "watching"
    /\ subscriber = "executor"
    /\ mergeLedger = [w \in Watches |-> 0]
    /\ lastFromVerdict = "watching"
    /\ lastFromRevision = 0
    /\ lastToVerdict = "watching"
    /\ lastToRevision = 0
    /\ blockClearReason = ""

\* Record the from/to pair of a transition, relative to the current Watch.
Record(fromV, fromR, toV, toR) ==
    /\ lastFromVerdict' = fromV
    /\ lastFromRevision' = fromR
    /\ lastToVerdict' = toV
    /\ lastToRevision' = toR

\* The already-merged ledger guard. When MERGE_ONCE is FALSE the guard is
\* removed, which is the control patch for NoDoubleMerge.
MergeAllowed == ~MERGE_ONCE \/ mergeLedger[watch] = 0
IncMerge == [mergeLedger EXCEPT ![watch] = @ + 1]

\* ---------------------------------------------------------------------------
\* One CI evaluation. This is transitionVerdict from transition.go:
\*   - a verdict on a blocked/closed Watch is ignored (no LiveStatuses guard
\*     evaluation here),
\*   - same verdict -> no revision bump,
\*   - changed verdict -> revision + 1 and a status update,
\*   - terminal verdict -> closed, watching -> watching, else -> notifying.
\* ---------------------------------------------------------------------------
Emit(v) ==
    /\ status \in LiveStatuses
    /\ v /= verdict \/ ~VERDICT_REVISION
    /\ \/ v /= "merged"
       \/ MergeAllowed
    /\ verdict' = v
    /\ revision' = revision + (IF VERDICT_REVISION THEN 1 ELSE 0)
    /\ revision' \in Revisions
    /\ IF v \in TerminalVerdicts
       THEN status' = "closed"
       ELSE IF v = "watching"
            THEN status' = "watching"
            ELSE status' = "notifying"
    /\ mergeLedger' = IF v = "merged" THEN IncMerge ELSE mergeLedger
    /\ UNCHANGED <<watch, subscriber>>
    /\ blockClearReason' = ""
    /\ Record(verdict, revision, v, revision')

\* Labeled actions, one per verdict row of internal/domain/verdict.go.
EvaluateWatching    == Emit("watching")
EvaluateReady       == Emit("ready")
EvaluateRebase      == Emit("rebase")
EvaluateRetry       == Emit("retry")
EvaluateKeepWorking == Emit("keep_working")
EvaluateTimeout     == Emit("timeout")
EvaluateMerged      == Emit("merged")
EvaluateClosed      == Emit("closed")
EvaluateBlocked     == Emit("blocked")
EvaluateRecheck     == Emit("recheck")

\* applyMerge on verdict=ready with autoMerge. Guarded by verdict and status
\* as well as the ledger; this is the ordinary successful-merge path.
DoMerge ==
    /\ status = "notifying"
    /\ verdict = "ready"
    /\ Emit("merged")

\* A repeated CI MERGED observation or a retried merge call. It has no live
\* status guard on purpose: the ledger is the only thing that stops a second
\* successful merge, exactly as closeWatch returns 0 when the transition bumps
\* no revision. This is the path the NoDoubleMerge control patch exercises.
ReobserveMerged ==
    /\ MergeAllowed
    /\ status' = "closed"
    /\ verdict' = "merged"
    /\ revision' = revision + (IF VERDICT_REVISION THEN 1 ELSE 0)
    /\ revision' \in Revisions
    /\ mergeLedger' = IncMerge
    /\ UNCHANGED <<watch, subscriber>>
    /\ blockClearReason' = ""
    /\ Record(verdict, revision, "merged", revision')

\* blockWatch: a known reason moves a live Watch to blocked and bumps revision.
Block ==
    /\ status \in LiveStatuses
    /\ status' = "blocked"
    /\ verdict' = "blocked"
    /\ revision' = revision + 1
    /\ revision' \in Revisions
    /\ UNCHANGED <<watch, subscriber, mergeLedger>>
    /\ blockClearReason' = ""
    /\ Record(verdict, revision, "blocked", revision')

\* unblockWatch / beacon_ack decision="blocked:resolved". The decision is the
\* only way out of blocked; a CI verdict is ignored while blocked.
ClearBlock(reason) ==
    /\ status = "blocked"
    /\ BLOCK_RESOLVED_ONLY => reason = "blocked:resolved"
    /\ status' = "watching"
    /\ verdict' = "watching"
    /\ revision' = revision + 1
    /\ revision' \in Revisions
    /\ blockClearReason' = reason
    /\ UNCHANGED <<watch, subscriber, mergeLedger>>
    /\ Record(verdict, revision, "watching", revision')

\* A head change closes a Watch and creates a successor with a fresh revision
\* counter and its own ledger entry (ADR 0019). History is relative to the new
\* Watch, so Record uses 0 on both sides.
SwitchWatch ==
    /\ watch' \in Watches \ {watch}
    /\ status' = "watching"
    /\ revision' = 0
    /\ verdict' = "watching"
    /\ subscriber' \in Subscribers
    /\ UNCHANGED mergeLedger
    /\ blockClearReason' = ""
    /\ Record("watching", 0, "watching", 0)

Next ==
    \/ EvaluateWatching
    \/ EvaluateReady
    \/ EvaluateRebase
    \/ EvaluateRetry
    \/ EvaluateKeepWorking
    \/ EvaluateTimeout
    \/ EvaluateMerged
    \/ EvaluateClosed
    \/ EvaluateBlocked
    \/ EvaluateRecheck
    \/ DoMerge
    \/ ReobserveMerged
    \/ Block
    \/ \E r \in {"blocked:resolved", "timeout", "ready", "watching"} :
        ClearBlock(r)
    \/ SwitchWatch

Spec == Init /\ [][Next]_vars

\* ---------------------------------------------------------------------------
\* Invariants. Each control patch makes exactly one of these reachable.
\* ---------------------------------------------------------------------------
NoDoubleMerge == \A w \in Watches : mergeLedger[w] <= 1

VerdictNeedsRevision ==
    lastToVerdict = lastFromVerdict \/ lastToRevision > lastFromRevision

BlockClearedOnlyByResolved ==
    blockClearReason \in {"", "blocked:resolved"}
====
