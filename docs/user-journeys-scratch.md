# gitrdone User Journeys — Scratchpad

**Status:** Working notes, not a settled product contract.

The purpose of this document is to preserve journey inputs and executable scenarios while the client experience is being worked out. Settled decisions belong in `vision.md` or `reservations-and-resolutions.md`.

## Journey dimensions

### Starting states

- bootstrap a fresh repository;
- induct an existing repository and choose its first accepted intent;
- join a repository already managed by gitrdone;
- attach or replace a human or agent workspace;
- resume an existing workstream.

### Actors and authority

Humans and agents are both principals. The relevant distinction is what authority they hold: proposer, responsible owner, requested reviewer, judgement agent, amendment agent, repository operator, or emergency authority.

### Proposed change shapes

- one commit representing one deliverable;
- several commits representing one deliverable;
- one commit containing concerns that should be split;
- a new version of an existing change;
- dependent or stacked changes;
- proposals that should be combined, withdrawn, or superseded.

Commit boundaries are evidence, not necessarily the repository's final judgement boundary.

### Content characteristics

- code;
- peripheral repository files;
- binaries or other opaque content;
- agent guidance and skills;
- purpose, priorities, or other governing files;
- Differ-bound or otherwise deployable content requiring specialised evidence.

Most content categories select judgement capabilities rather than create different client journeys. Opaque content, governing files, and deployable artifacts may materially change the experience.

### Judgement paths

- promote immediately;
- gather evidence, then promote or reject;
- amend and reconsider;
- defer to a human because issues were found;
- defer to a human because policy or judgement requires it despite no issue;
- notify without blocking;
- split, combine, hold, or reject;
- preserve a conflict for further judgement.

### Continuing work and reconciliation

- continue locally while a proposal is held;
- build and submit a dependent change;
- absorb promotion, rejection, amendment, or replacement of a dependency;
- reconcile server amendments with ongoing descendants;
- explain what happened, why, and what the user must do next.

### Failure, recovery, and exceptional authority

- stale base or concurrently advanced intent;
- semantic or mechanical conflict;
- unavailable tool, agent, simulator, or reviewer;
- inconclusive or looping judgement;
- timed-out and retried submission;
- post-promotion regression and repair or revert;
- explicit emergency promotion under bounded authority and audit.

Urgency is input to judgement, not a contributor-controlled guarantee of promotion. Emergency promotion must be an explicit repository operation rather than an undocumented route around accepted intent.

## First executable journeys

1. Submit and promote immediately.
2. Submit, hold, continue working, and stack another change.
3. Submit, receive a repository amendment, and reconcile ongoing work.

For each journey, work out:

- what the user does;
- what the repository does;
- what the user sees immediately;
- what local state remains usable;
- what may happen asynchronously;
- how the outcome returns;
- how plain Git, a thin `grd` client, and a richer native or jj-aware client differ.

## How to use the matrix

The matrix is a product decision aid and an eventual acceptance-test plan. It is not a promise that every variant belongs in the first implementation slice.

Each journey has one baseline. Add a deviation only when it changes the user contract, repository mechanics, or required client capability. Do not enumerate every possible combination of actor, content, and outcome.

Dispositions mean:

- **First proof:** must work in the first executable version of the journey.
- **Initial boundary:** the first version should refuse or constrain this case truthfully without damaging work.
- **Native target:** required for the intended product experience, but not necessarily the first proof.
- **Open:** the desired behaviour still needs to be worked out.

The matrix describes intended journeys, not the behaviour of the currently deployed Git server.

## Journey 1: submit and promote immediately

Baseline:

```text
Accepted intent: A
Workspace: clean B based on A
User submits B
Repository durably admits B
No-op judgement promotes B
Accepted intent advances A → B
Workspace remains usable
```

| ID | Deviation | Expected user contract | Disposition |
|---|---|---|---|
| J1.0 | Clean workspace, one commit, directly ahead of A | `grd submit` admits and immediately promotes B; the response reports both facts; local work is not destructively changed | First proof |
| J1.1 | Several commits form one deliverable | Submit them as one change version when the user selects or confirms that boundary | Native target |
| J1.2 | Workspace contains uncommitted content | Do not guess whether the content belongs in the proposal; preserve it and explain the boundary required | Initial boundary |
| J1.3 | Workspace is based on older accepted intent | Do not present the proposal as current; either reconsider it against current intent or give a safe reconciliation path | Open |
| J1.4 | Workspace has mechanically diverged from accepted intent | Preserve all work and expose that reconciliation is needed; never overwrite either side | Initial boundary |
| J1.5 | The same submission is retried after a lost response | Return the same admitted change/version and promotion rather than creating duplicates | First proof |
| J1.6 | One submitted snapshot contains independently treatable concerns | Admit it without pretending commit boundaries settle the issue; judgement may later split it with preserved provenance | Native target |
| J1.7 | Content cannot be inspected fully, such as an opaque binary | Admission can still succeed, but judgement must expose its evidence limits and may choose a different consequence | Native target |
| J1.8 | An agent submits instead of a human | The mechanics remain the same; authority and provenance determine what the agent may do | Native target |

Candidate thin-client transcript:

```text
$ grd submit

Submitted: fix login timeout
Promoted
You can keep working.
```

Stable semantic rule: successful submission means durable admission. Promotion may also finish and be reported during the interaction, but clients must not require every judgement to finish synchronously.

## Journey 2: submit, hold, continue, and stack

Baseline:

```text
Accepted intent: A
User submits B
Judgement holds B
Workspace advances to new work C based on B
User submits C with an explicit dependency on B
Repository admits C without pretending B is accepted intent
```

| ID | Deviation | Expected user contract | Disposition |
|---|---|---|---|
| J2.0 | B remains held while C is created and submitted | The user can keep working; C is admitted as dependent on B; independent inspection may proceed while promotion waits | First proof for Journey 2 |
| J2.1 | B promotes unchanged before C is submitted | The dependency becomes satisfied by accepted intent and C can be considered against the new intent | Native target |
| J2.2 | B promotes unchanged after C is submitted | C advances from waiting-on-B to judgement against the new accepted intent without being resubmitted | Native target |
| J2.3 | B is amended while C depends on it | Preserve the original lineage and reconcile or reconsider C against the amended B; this continues in Journey 3 | Native target |
| J2.4 | B is rejected | Do not discard C; explain that its dependency failed and offer to withdraw it, revise it, or derive an independent version | Native target |
| J2.5 | The user revises B while C exists | Create a new immutable version of B and make C's relationship to that version explicit; do not silently retarget it | Open |
| J2.6 | Several sibling changes depend on B | Keep each change independently identifiable and judgeable while sharing the dependency | Native target |
| J2.7 | Proposed dependencies form a cycle | Reject the invalid dependency declaration without losing proposed content | Initial boundary |

Candidate thin-client transcript:

```text
$ grd submit -m "refactor authentication"

Submitted: refactor authentication
Admitted; judgement pending
Continue working on top of it.

$ grd status

Working change: add passkey support
Depends on:
  refactor authentication — judgement pending

$ grd submit -m "add passkey support"

Submitted: add passkey support
Status: waiting on "refactor authentication"
Independent inspection can continue meanwhile.
```

Settled UX rule: submitting freezes the current change version and lets the user continue on top of it. The Git adapter does not manufacture an empty successor commit; the next commit becomes successor work. Revising the submitted change is explicit rather than inferred from later edits.

Continuing a workstream and starting a new one deliberately have different bases:

- continuing after submitting B starts successor C on B, including while B is held;
- starting a new workstream starts from current accepted intent;
- the client should not prompt after every submission or force users to manipulate change IDs;
- a Git adapter may temporarily use a commit as the content boundary, but commit is not the native submission operation.

## Journey 3: repository amendment and reconciliation

Baseline:

```text
User submits B
User continues with C based on B
Repository creates amended version B′
Repository promotes B′
Repository and client reconcile C onto B′ as C′
User sees what changed, why, and what happened to ongoing work
```

| ID | Deviation | Expected user contract | Disposition |
|---|---|---|---|
| J3.0 | C can be replayed cleanly onto B′ | Reconcile automatically, preserve an undo path, and explain the amendment and descendant rewrite | Executable first proof |
| J3.1 | The user has no descendants of B | Move the clean workspace directly from B to accepted B′, preserve B as an undo path, and show the amendment without claiming that successor work was replayed | Executable proof |
| J3.2 | C conflicts mechanically with B′ | Preserve B, B′, and C; surface the conflict as durable work requiring judgement rather than destroying the workspace | Executable proof |
| J3.3 | B′ is still being judged rather than promoted | Show that the proposal has a new repository-produced version and why, without misrepresenting it as accepted intent or moving the workspace | Executable proof |
| J3.4 | The repository amends B more than once | Preserve every version and rationale, but avoid forcing the user through intermediate local churn that has no actionable value | Open |
| J3.5 | The original version carried a signature | Preserve the original signature and attribution; represent the repository amendment as a separately authored version | Native target |
| J3.6 | The user independently revises B while the repository creates B′ | Preserve both descendants as competing versions and require an explicit judgement or combination; do not guess identity | Open |
| J3.7 | The client loses contact midway through reconciliation | Retry from durable identities and return the same result; never duplicate or partially forget a rewrite | Native target |

Candidate thin-client transcript:

```text
$ grd sync

Your change "fix login timeout" was amended and promoted.

Repository amendment:
  Added bounded retry handling
  Reason: timeout path could duplicate the operation

Your dependent work was rebased successfully.
```

Journey 3 is expected to expose the clearest capability gradient: a jj-aware client may represent change evolution directly; a thin `grd` client over Git may need guarded rewrites and recovery refs; plain Git may only support a reduced, more manual reconciliation experience.

The executable Git-adapter proofs keep B and B′ as immutable versions of one change and record the repository rationale. For J3.0, the ordinary judgement path promotes B′ and `grd sync` replays a clean local C onto it. For J3.1, a clean workspace still at B moves directly to accepted B′ without invoking descendant replay. Both reconciliation paths first create `refs/grd/recovery/<original-head>`. For J3.2, a mechanical replay conflict restores C, admits it with Change/Version identity, and records durable B → B′ reconciliation work for judgement without moving accepted intent or trunk. Git-reported paths are diagnostics; jj-core remains the intended owner of first-class conflicted C′ content and descendant rebasing. For J3.3, B′ remains in judgement: `grd status` explains the pending repository amendment, while accepted intent, canonical trunk, and the workspace remain unchanged and `grd sync` refuses reconciliation. An already admitted dependent C still fails amendment closed until J2.3 exists; multiple unseen amendments and retry after interruption remain the later J3 deviations above.

## Working order

1. Settle the complete J1.0 transcript and state transitions.
2. Turn J1.0 and J1.5 into executable acceptance scenarios.
3. Decide the initial boundaries for J1.2 and J1.4.
4. Use J2.0 to test whether the submission boundary and automatic-successor hypothesis are coherent.
5. Use J3.0 and J3.2 to determine what client and engine capabilities the native experience actually requires.
6. Revisit the remaining variants only when a baseline journey or implementation decision reaches them.
