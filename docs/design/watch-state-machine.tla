---- MODULE WatchStateMachine ----
EXTENDS Naturals, FiniteSets

\* One Watch identity. The successor created by head_changed has a new ID and
\* a fresh ledger, so it is a separate model run, not another merge of W.
W == "watch-1"
MaxRev == 3

Statuses == {"watching", "notifying", "blocked", "parked", "needs_input", "closed"}
Verdicts == {"watching", "ready", "rebase", "retry", "keep_working", "timeout",
             "blocked", "merged", "closed", "recheck", "wait_dependency",
             "needs_input", "parked", "accepted"}
Phases == {"watching", "notified", "finished"}
Live == {"watching", "notifying"}
Actionable == {"ready", "rebase", "retry", "keep_working", "timeout",
               "recheck", "wait_dependency"}
Terminal == {"merged", "closed"}
RevRange == 0..MaxRev

\* The first five variables are the card's required state. The last three are
\* bounded history variables. They make rules 2 and 3 expressible as state
\* invariants instead of action properties.
VARIABLES watch, revision, verdict, subscriber, mergeLedger,
          verdictLog, blockEpoch, resolveCount

\* NewRev is the one place that advances a revision. The silent-verdict control
\* patch edits this definition to keep the revision fixed.
NewRev == revision + 1
Room == revision < MaxRev

TypeOK ==
  /\ watch \in Statuses
  /\ revision \in RevRange
  /\ verdict \in Verdicts
  /\ subscriber \in Phases
  /\ mergeLedger \subseteq {W} \X RevRange
  /\ verdictLog \subseteq (RevRange \X Verdicts)
  /\ blockEpoch \in 0..MaxRev
  /\ resolveCount \in 0..MaxRev

Init ==
  /\ watch = "watching"
  /\ revision = 0
  /\ verdict = "watching"
  /\ subscriber = "watching"
  /\ mergeLedger = {}
  /\ verdictLog = {<<0, "watching">>}
  /\ blockEpoch = 0
  /\ resolveCount = 0

Keep ==
  UNCHANGED <<watch, revision, verdict, subscriber, mergeLedger,
               verdictLog, blockEpoch, resolveCount>>

\* domain.transitionVerdict: a changed verdict bumps the revision once and
\* publishes the same verdict to the subscriber at that revision.
Publish(v, s) ==
  /\ Room
  /\ revision' = NewRev
  /\ verdict' = v
  /\ watch' = s
  /\ subscriber' = IF v \in Terminal THEN "finished"
                   ELSE IF v \in (Actionable \cup {"blocked"}) THEN "notified"
                   ELSE subscriber
  /\ verdictLog' = verdictLog \cup {<<NewRev, v>>}
  /\ mergeLedger' = mergeLedger
  /\ blockEpoch' = blockEpoch
  /\ resolveCount' = resolveCount

\* domain.EvaluatePR rows 9 and 10 and cibridge.publishRecheck.
VerdictWatching ==
  \/ /\ watch \in Live /\ verdict = "watching" /\ Keep
  \/ /\ watch \in Live /\ verdict # "watching" /\ Publish("watching", "watching")

VerdictRebase == /\ watch \in Live /\ verdict # "rebase" /\ Publish("rebase", "notifying")
VerdictRetry == /\ watch \in Live /\ verdict # "retry" /\ Publish("retry", "notifying")
VerdictKeepWorking == /\ watch \in Live /\ verdict # "keep_working" /\ Publish("keep_working", "notifying")
VerdictTimeout == /\ watch \in Live /\ verdict # "timeout" /\ Publish("timeout", "notifying")
VerdictRecheck == /\ watch \in Live /\ verdict # "recheck" /\ Publish("recheck", "notifying")
VerdictWaitDependency == /\ watch \in Live /\ verdict # "wait_dependency" /\ Publish("wait_dependency", "notifying")
VerdictReady == /\ watch \in Live /\ verdict # "ready" /\ Publish("ready", "notifying")
VerdictClosed == /\ watch \in Live /\ verdict # "closed" /\ Publish("closed", "closed")

\* cibridge.applyBlocked -> domain.blockWatch. Every block starts a new epoch,
\* so an earlier resolve cannot clear it.
Block ==
  /\ watch \in Live
  /\ verdict # "blocked"
  /\ Room
  /\ watch' = "blocked"
  /\ verdict' = "blocked"
  /\ subscriber' = "notified"
  /\ revision' = NewRev
  /\ verdictLog' = verdictLog \cup {<<NewRev, "blocked">>}
  /\ blockEpoch' = blockEpoch + 1
  /\ UNCHANGED <<mergeLedger, resolveCount>>

\* A blocked:resolved decision reaching the control plane. It changes no Watch
\* revision; the following Unblock applies domain.unblockWatch.
ResolveSignal ==
  /\ watch = "blocked"
  /\ resolveCount < MaxRev
  /\ resolveCount' = resolveCount + 1
  /\ UNCHANGED <<watch, revision, verdict, subscriber, mergeLedger,
                  verdictLog, blockEpoch>>

Unblock ==
  /\ watch = "blocked"
  /\ resolveCount >= blockEpoch
  /\ Room
  /\ watch' = "watching"
  /\ verdict' = "watching"
  /\ subscriber' = "notified"
  /\ revision' = NewRev
  /\ verdictLog' = verdictLog \cup {<<NewRev, "watching">>}
  /\ UNCHANGED <<mergeLedger, blockEpoch, resolveCount>>

\* applyVerdict returns early for a closed Watch and the ledger is the second
\* guard against a repeated auto-merge. The double-merge control patch deletes
\* this whole conjunct.
NotAlreadyMerged == watch \in Live /\ mergeLedger = {}

Merge ==
  /\ NotAlreadyMerged
  /\ Room
  /\ watch' = "closed"
  /\ verdict' = "merged"
  /\ subscriber' = "finished"
  /\ revision' = NewRev
  /\ mergeLedger' = mergeLedger \cup {<<W, NewRev>>}
  /\ verdictLog' = verdictLog \cup {<<NewRev, "merged">>}
  /\ UNCHANGED <<blockEpoch, resolveCount>>

\* Terminal-ish lifecycle events that do not change the verdict.
HeadChanged ==
  /\ watch # "closed"
  /\ watch' = "closed"
  /\ subscriber' = "finished"
  /\ UNCHANGED <<revision, verdict, mergeLedger, verdictLog, blockEpoch, resolveCount>>

Cancel ==
  /\ watch # "closed"
  /\ watch' = "closed"
  /\ subscriber' = "finished"
  /\ UNCHANGED <<revision, verdict, mergeLedger, verdictLog, blockEpoch, resolveCount>>

Accept ==
  /\ watch # "closed"
  /\ Room
  /\ watch' = "closed"
  /\ verdict' = "accepted"
  /\ subscriber' = "finished"
  /\ revision' = NewRev
  /\ verdictLog' = verdictLog \cup {<<NewRev, "accepted">>}
  /\ UNCHANGED <<mergeLedger, blockEpoch, resolveCount>>

\* ADR 0031 and ADR 0039 parking paths. They bump the revision because the
\* verdict changes, but no subscriber wake is staged by the resume itself.
NeedsInput ==
  /\ watch \in Live
  /\ Room
  /\ watch' = "needs_input"
  /\ verdict' = "needs_input"
  /\ revision' = NewRev
  /\ verdictLog' = verdictLog \cup {<<NewRev, "needs_input">>}
  /\ UNCHANGED <<subscriber, mergeLedger, blockEpoch, resolveCount>>

Continue ==
  /\ watch = "needs_input"
  /\ Room
  /\ watch' = "watching"
  /\ verdict' = "watching"
  /\ subscriber' = "notified"
  /\ revision' = NewRev
  /\ verdictLog' = verdictLog \cup {<<NewRev, "watching">>}
  /\ UNCHANGED <<mergeLedger, blockEpoch, resolveCount>>

Park ==
  /\ (watch \in Live \/ watch = "needs_input")
  /\ Room
  /\ watch' = "parked"
  /\ verdict' = "parked"
  /\ revision' = NewRev
  /\ verdictLog' = verdictLog \cup {<<NewRev, "parked">>}
  /\ UNCHANGED <<subscriber, mergeLedger, blockEpoch, resolveCount>>

Unpark ==
  /\ watch = "parked"
  /\ Room
  /\ watch' = "watching"
  /\ verdict' = "watching"
  /\ subscriber' = "notified"
  /\ revision' = NewRev
  /\ verdictLog' = verdictLog \cup {<<NewRev, "watching">>}
  /\ UNCHANGED <<mergeLedger, blockEpoch, resolveCount>>

\* A closed Watch has no further transitions. Stuttering keeps the next-state
\* relation total so TLC's deadlock check still catches an unintended trap.
Stutter ==
  /\ watch = "closed"
  /\ Keep

Next ==
  \/ Stutter
  \/ VerdictWatching
  \/ VerdictRebase
  \/ VerdictRetry
  \/ VerdictKeepWorking
  \/ VerdictTimeout
  \/ VerdictRecheck
  \/ VerdictWaitDependency
  \/ VerdictReady
  \/ VerdictClosed
  \/ Block
  \/ ResolveSignal
  \/ Unblock
  \/ Merge
  \/ HeadChanged
  \/ Cancel
  \/ Accept
  \/ NeedsInput
  \/ Continue
  \/ Park
  \/ Unpark

vars == <<watch, revision, verdict, subscriber, mergeLedger,
          verdictLog, blockEpoch, resolveCount>>

Spec == Init /\ [][Next]_vars

\* Rule 1: a Watch has at most one confirmed merge in its ledger.
NoDoubleMerge == Cardinality(mergeLedger) <= 1

\* Rule 2: one revision cannot carry two different verdicts. A verdict change
\* that skips the revision bump writes a second pair at the same revision.
NoSilentVerdictChange ==
  \A r \in RevRange :
    \A v1, v2 \in Verdicts :
      (<<r, v1>> \in verdictLog /\ <<r, v2>> \in verdictLog) => v1 = v2

\* Rule 3: once a block epoch starts, the Watch may be live again only after a
\* blocked:resolved signal for that epoch.
BlockLiftedOnlyAfterResolve ==
  watch \notin Live \/ resolveCount >= blockEpoch
=============================================================================
