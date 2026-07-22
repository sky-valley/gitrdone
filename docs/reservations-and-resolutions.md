# gitrdone: Reservations and Resolutions

## Purpose

This is the living record of material reservations raised while working out the gitrdone architecture and the resolutions agreed by Noam and the coding agent.

The point is not to accumulate objections. Each reservation should become one of:

- a settled resolution;
- an explicit experiment with a decision criterion;
- a genuinely open question with candidate ways forward.

Read the canonical [vision](vision.md) first. This document records how difficult parts of that vision become implementable without weakening the mission or modularity.

## Resolution 001: gitrdone owns logical change identity

**Status:** Agreed

### Reservation

The original architecture depended on jj change identity as the idempotency key for the whole system while also requiring modularity across VCS engines and compatibility with ordinary Git clients.

That creates two problems:

1. If jj owns the product's canonical identity, the judgement domain becomes coupled to its first VCS engine.
2. An ordinary Git client does not necessarily preserve or transmit a stable change identity after a local amend or rebase.

Git commit identity is also unsuitable because a commit hash identifies one immutable representation, not an evolving thread of intent.

### Resolution

gitrdone owns the repository's canonical logical change identity.

- A **Change** is the durable thread of evolving intent.
- A **Version** is one immutable concrete realisation of that change.
- A **Submission** records an attempt by a producer to introduce a version to the repository.
- An **External Identity** maps a gitrdone change or version to substrate-specific objects such as jj change IDs, Git commit IDs, or GitHub pull requests.

The conceptual shape is:

```text
Change grd_change_123
├── Version 1: submitted by Ion
├── Version 2: amended by a review agent
└── Version 3: rebased onto newer canonical intent
```

Each version must retain enough information to establish:

- its content or engine representation;
- its parent version or derivation;
- the canonical intent against which it was produced or evaluated;
- its dependencies on other changes or versions;
- the human or agent that produced it;
- provenance, attribution, and relevant signatures;
- substrate-specific identities;
- why the version was created.

### Native operation boundary

The current conceptual submission operation is:

```text
propose(
  changeID?,
  baseIntent,
  dependencies,
  content,
  producer,
  context
) -> submission receipt
```

`propose` records a candidate version and returns a receipt. It does not move canonical intent.

Judgement, promotion, and reconciliation are separate operations:

```text
judge(version) -> evidence + outcome
promote(version, expectedIntent) -> new canonical intent
reconcile(workspaceCursor) -> accepted changes + rewritten versions + explanations
```

These operations belong to gitrdone's domain. Git, jj, GitHub, and future clients or engines adapt onto them rather than defining them.

### Engine and adapter consequences

- jj change IDs remain valuable engine-level identities and may be bound to gitrdone changes.
- Git commit IDs identify immutable versions or representations, not logical changes.
- A native or jj-aware client can explicitly update an existing change.
- A plain Git submission without continuity metadata normally creates a new change.
- An adapter may suggest or ask to bind related submissions, but heuristic similarity must not be presented as guaranteed identity.
- Bot amendments create new immutable versions of the same logical change.
- Rebasing or conflict resolution creates a new version and preserves its derivation.

### Modularity boundary

The agreed ownership rule is:

> gitrdone owns logical change identity; VCS engines own concrete repository representations.

This allows gitrdone to use jj's richer capabilities without making jj the product's identity or forcing weaker substrates to pretend they provide guarantees they do not have.

### Remaining edges

This resolution does not yet settle:

- when an explicitly supplied `changeID` is authorised to update an existing change;
- whether rebasing creates a version automatically or only when content changes;
- how split and combined changes inherit identity and provenance;
- how external identities are bound, superseded, or disputed;
- the exact idempotency key for retrying one submission request;
- what canonical intent contains and what promotion changes.

## Resolution 002: canonical intent is accepted content

**Status:** Agreed

### Reservation

The first proposed `IntentRevision` mixed several responsibilities: accepted content, governing context, evidence, rationale, actor information, timestamps, and VCS representation. That made the central object too fat for the first slice and blurred the difference between repository state and the decision that produced it.

There was also an open question about whether canonical intent meant repository content alone or repository content plus its governing policy.

### Resolution

Canonical intent is the repository's accepted content.

For the first slice, an intent revision is deliberately small:

```text
IntentRevision
  id
  previousIntent
  contentRef
```

It answers only:

> What exact repository content is accepted now, and which accepted state preceded it?

Intent revision history is linear even when the underlying VCS graph is not:

```text
Intent 41 → Intent 42 → Intent 43
```

### Content reference

`contentRef` is the immutable engine representation of the accepted repository content. Separate `contentRoot` and `engineRepresentation` fields are not needed initially.

Example serialized forms are:

```text
git:abc123
jj:def456
```

Internally, the same value may be represented structurally:

```text
ContentRef
  engine
  revision
```

The engine owns how to materialize its revision. gitrdone treats the reference as an immutable representation without interpreting engine-specific identity.

An engine-neutral content digest may be added later if gitrdone needs to prove that two different engine references represent identical content. It is not required for the first slice.

### Judgement and promotion remain separate

The explanation and authority for a transition do not belong inside `IntentRevision`.

```text
Judgement
  governingIntent
  evidence
  rationale
  actor
```

```text
Promotion
  fromIntent
  toIntent
  promotedVersions
  judgement
```

- **IntentRevision** records the accepted content.
- **Judgement** records why proposed content is acceptable under a particular governing snapshot.
- **Promotion** records the transition from one accepted state to another.

Trunk is a VCS projection of the current intent revision. Moving that projection is a consequence of promotion, not the source of product truth.

### Governance semantics

Governance is the versioned lens and authority under which content becomes canonical. It is not a separate part of the `IntentRevision` identity.

Purpose and priorities are text files in the repository. A candidate is judged using those files from the current accepted intent, not modified versions contained in that same candidate.

Example:

```text
Intent 41 contains priorities v7

Candidate proposes application changes and priorities v8
Candidate is judged using priorities v7

If promoted:
Intent 42 contains priorities v8
Future candidates use priorities v8
```

Judgements reference the accepted intent from which their governing files were read; they do not duplicate those files into a separate governance object.

### Dependency semantics

Dependencies belong to proposed change versions, not intent revisions.

By the time promotion creates a new intent revision, every dependency must have been:

- promoted in the same transition;
- already satisfied by canonical content; or
- removed by deriving an independent version.

Canonical intent never depends on unpromoted holding-bay state in order to remain valid.

### Agreed ownership rule

> Intent is accepted content. Judgement explains why it is acceptable. Promotion connects the previous accepted content to the new accepted content.

### Remaining edges

This resolution does not yet settle:

- whether intent revision IDs are generated or content-derived;
- whether one promotion may contain several change versions in the first slice;
- the exact transaction and retry semantics of promotion;
- when evidence must be refreshed after canonical intent advances;
- the filenames and discovery convention for purpose and priorities.

## Resolution 003: proposing creates a change version

**Status:** Agreed

### Reservation

The initial model treated `Submission` as a first-class product object in addition to `Change` and `ChangeVersion`. That risked promoting network attempts, retries, and transport details into the central domain before they had demonstrated independent product meaning.

There was also a question about whether a change version needed separate representations for complete content and the delta from its base.

### Resolution

For the first slice, `propose` is a command that creates an immutable `ChangeVersion`. There is no first-class `Submission` domain object.

Conceptually:

```text
propose(
  changeID?,
  baseIntent,
  contentRef,
  dependencies,
  producer,
  idempotencyKey
) -> ChangeVersion
```

The command:

1. creates a `Change` when `changeID` is absent;
2. creates an immutable `ChangeVersion`;
3. makes that version available for judgement;
4. returns the resulting identity.

The minimal product shape is:

```text
Change
  id
```

```text
ChangeVersion
  id
  change
  previousVersion?
  baseIntent
  contentRef
  dependencies
  producer
```

### Retry and transport bookkeeping

Request attempts and retries are infrastructure facts. An idempotency record may map a request key to the resulting `ChangeVersion` so that a timed-out request can be retried safely.

If submission attempts later acquire independent product meaning, gitrdone may add a `SubmissionAttempt` record without changing the change identity model.

A Git push may contain several commits and ref updates. The Git adapter is responsible for mapping that transport batch onto one or more native `propose` commands. The native operation does not become Git-push-shaped merely to accommodate that adapter.

### Content semantics

`contentRef` identifies the complete immutable repository state proposed by the version. `baseIntent` identifies the accepted content from which the proposal was made.

```text
ChangeVersion
  baseIntent: A
  contentRef: B
```

The VCS engine can compare A with B to derive the proposed change. If canonical intent later advances to C, the engine can attempt to replay that derived change onto C and produce a new version.

No separate patch or delta field is required for the first slice.

### Holding semantics

The holding bay is initially derived from durable facts rather than represented by a large mutable lifecycle enum.

A version may exist without promotion, receive a hold or rejection judgement, be superseded by a newer version, or be included in a promotion. Operational queue states may exist in worker infrastructure without becoming the product's source of truth.

### Agreed ownership rule

> `propose` creates a change version. Transport attempts are infrastructure bookkeeping. The version's base and content reference are sufficient to recover the proposed change.

### Remaining edges

This resolution does not yet settle:

- who is authorised to add a version to an existing change;
- how dependencies are represented and validated;
- when a proposed version enters the judgement queue;
- how a Git adapter divides a multi-commit push into logical changes;
- whether rejected pre-admission attempts need a security audit record.

## Resolution 004: judgement is a live adaptive process

**Status:** Agreed

### Reservation

Treating judgement as a single verdict—approve, hold, or reject—would preserve the basic shape of pull requests and static CI while merely adding an agent at the front. Treating triage as a generator for a complete fixed pipeline would reproduce GitHub Actions dynamically rather than escaping its constraints.

The product needs to support materially different treatments for different proposals, including:

- promote immediately;
- test, then promote;
- review with a skill, amend, retest, then promote;
- amend, test, and request human approval;
- notify a human without blocking;
- invoke a simulator only for relevant changes;
- split, reject, or gather further evidence when appropriate.

### Resolution

Every proposal opens a live judgement process. Judgement is not a one-time verdict and not a statically planned pipeline.

Triage repeatedly chooses what should happen next by considering:

- the current change version;
- the repository's purpose and priorities;
- the trusted governing snapshot;
- evidence and actions already completed;
- available skills, tools, agents, simulators, and human reviewers;
- uncertainty introduced by amendments or changes in canonical intent.

After each action, result, amendment, or human response, triage may reconsider the appropriate next actions.

Example:

```text
Proposal V1
  → lint
  → skill X review
  → review finds a problem
  → amendment creates V2
  → triage V2
  → rerun lint
  → run UI simulator
  → promote V2
```

A different proposal may receive different treatment:

```text
Proposal V1
  → detect data-model change
  → lint
  → skill X review
  → one amendment round
  → request Noam's approval of the current version
  → promote after approval
```

### Guidance and proposal-specific obligations

Repository guidance expresses purposes and priorities such as:

- “Noam wants to review only when there are data-model changes.”
- “All changes must be linted when the language supports it.”
- “All changes must be reviewed by skill X with one round of amendments.”
- “UI modifications must go through the simulator.”

The triage agent interprets that guidance against the actual proposal. When guidance applies, it becomes a recorded obligation for that judgement.

For example:

```text
Applicable obligations for V2
  lint: required
  skill X review: required
  amendment rounds: at most one
  simulator: required because V2 modifies UI
  Noam approval: not required because V2 has no data-model change
```

Triage may also choose additional proportionate actions when uncertainty or significance warrants them. Applicable obligations constrain the process without turning natural-language governance into a fixed global pipeline.

### Process and event history

The first model should remain small:

```text
Judgement
  id
  change
  governingIntent
```

The process is recorded as events tied to the version they concern:

```text
triaged V1
lint requested for V1
lint passed for V1
skill X review requested for V1
amendment created V2
triaged V2
simulator requested for V2
simulator passed for V2
promotion chosen for V2
```

The durable history explains which guidance was applied, what was requested, what happened, why the process changed direction, and which exact version earned an outcome. A large fixed workflow object is not required.

Conceptually, triage performs:

```text
decideNext(judgement, currentVersion, history)
  -> actions + rationale
```

Possible actions include:

- inspect;
- invoke a skill;
- lint or test;
- simulate;
- ask another agent;
- amend;
- split;
- request human approval;
- notify;
- promote;
- reject;
- gather further evidence.

### Authority boundary

The judgement agent is not merely advisory. The repository delegates semantic authority to the judgement process.

- The agent determines which guidance applies.
- It chooses proportionate next actions.
- It may amend and re-triage.
- It may escalate to a human, reject, or choose promotion.
- Its rationale and applied obligations are recorded.

A deterministic coordinator executes durable effects. It ensures that the chosen outcome concerns the current version, that declared obligations are not still incomplete, and that promotion advances the expected canonical intent atomically.

The ownership rule is:

> The judgement process determines what ought to happen. The coordinator makes the outcome durable and internally consistent.

### Version semantics

Judgement belongs to the continuing change and may span several immutable versions. Each piece of evidence is tied to the version it actually examined.

An amendment creates a new version. Triage then decides which prior evidence remains relevant and which actions must run again. Promotion and human approval always concern an exact version, not an ambiguous moving change.

### Agreed product rule

> Judgement is a live adaptive process that determines the appropriate treatment of each proposal, not a verdict and not a dynamically generated static pipeline.

### Remaining edges

This resolution leaves the following implementation questions open:

- the exact event and storage schemas;
- how evidence carries forward after a small amendment;
- how human approval is bound and invalidated when a version changes;
- what happens when a required tool, agent, skill, or simulator is unavailable;
- how multiple judgement agents disagree or combine their findings;
- how capabilities and skills are discovered, authorised, and invoked;
- whether obligations are stored explicitly or derived from the event history;
- how the process detects that it is looping or has exhausted its useful actions.

## Resolution 005: purpose and priorities are repository text

**Status:** Agreed

### Reservation

Introducing a separate governance store or rich `GovernanceSnapshot` model would duplicate state already versioned by the repository and create unnecessary questions about which system owns purpose and priorities.

### Resolution

Purpose and priorities are text files stored in the repository.

- They are versioned as ordinary repository content.
- The judgement process reads them from current canonical intent.
- A judgement records the `IntentRevision` from which its governing files were read.
- It does not need to copy those files into a separate governance object.
- Repository skills are likewise loaded from accepted repository content when they are used by judgement.

Conceptually:

```text
Intent 41
  content includes purpose and priorities v7

Candidate V1
  may include proposed purpose and priorities v8

Judgement of V1
  governed by the files in Intent 41

Promotion creates Intent 42
  content now includes purpose and priorities v8

Future judgements
  governed by the files in Intent 42
```

Candidate changes to purpose, priorities, or repository skills do not govern their own judgement. They become active only after promotion into canonical intent.

### Agreed ownership rule

> The repository contains its own purpose and priorities. Current accepted intent governs proposed intent.

### Remaining edges

This resolution does not yet settle:

- the exact filenames and discovery convention;
- the minimum required contents of a new repository;
- what happens when purpose or priorities are missing or malformed;
- whether changes to governing files always trigger human review;
- whether an in-progress judgement keeps its original governing intent or re-triages when canonical intent changes;
- how the first canonical intent is bootstrapped before governing files exist.

## Resolution 006: prove durability with a local journal first

**Status:** Agreed for the current implementation stage

### Reservation

The first operational plan called for a Postgres ledger. Postgres will eventually be useful for concurrent servers, querying across repositories, and operational scale, but introducing it before proving the repository mechanics would mix infrastructure work with the harder product question: whether intent, proposals, promotions, and retries form a truthful durable model.

Using mutable JSON files instead would be superficially simpler but would force gitrdone to coordinate atomic updates across several files and recreate database transactions badly.

### Resolution

The first durable ledger is one append-only filesystem journal per repository, behind a repository-owned ledger interface.

The journal records source events for:

- repository initialization and the first intent revision;
- proposals, including their change, version, and idempotency key;
- promotions and the successor intent revision.

Records are ordered, appended, and synced before they are treated as durable. Repository state is reconstructed by replay rather than by trusting a mutable snapshot. An incomplete final record may be removed during restart recovery; malformed complete records fail closed.

The current operating constraint is one journal writer per repository. The filesystem adapter enforces that constraint with an exclusive lock.

The ledger interface belongs to the intent domain. It exposes narrow queries and durable operations rather than a complete replay snapshot. The filesystem adapter may replay the journal into private indexes; a future Postgres adapter may answer the same queries directly. Neither adapter's hydration or storage representation belongs in the shared contract.

The filesystem format is an adapter, not part of the product identity. A future Postgres implementation should preserve the domain contract rather than leak relational storage concepts into repository judgement.

### Migration triggers

Move to a transactional service such as Postgres when the product needs one or more of:

- multiple gitrdone processes writing the same repository;
- cross-repository querying or coordination;
- operational indexing beyond replaying a repository-local history;
- volume at which journal replay or compaction becomes an observed problem;
- infrastructure guarantees that a local filesystem cannot honestly provide.

Do not migrate merely because a database is expected eventually.

### Crash edge

The journal cannot make a VCS ref update and a ledger append atomic. Resolution 007 defines the prepared-promotion protocol used to recover that boundary safely.

### Agreed ownership rule

> Prove repository durability with the smallest honest local ledger; change the storage adapter when observed coordination and scale require it.

## Resolution 007: promotions are prepared before projection

**Status:** Agreed and implemented for the current slice

### Reservation

Promotion crosses two independently durable systems: the intent ledger and the VCS trunk projection. Moving trunk before recording the transition can leave trunk at B while canonical intent remains A after a crash. Recording canonical intent first can produce the inverse lie.

### Resolution

A promotion allocates its promotion ID and successor intent ID before moving trunk, then follows this protocol:

```text
record prepared promotion A → B
compare-and-swap trunk A → B
record promotion completion and advance current intent to B
```

The prepared record contains the exact promotion, version, previous intent, successor intent, and target content required to resume the operation without allocating new identities.

Only one incomplete promotion is permitted per repository in this implementation stage. Proposal admission may continue while reconciliation is pending, but another promotion cannot begin.

### Restart reconciliation

When a prepared promotion is found, gitrdone reads the actual trunk projection:

- If trunk is A, retry the compare-and-swap to B and complete the promotion.
- If trunk is B, the projection already succeeded; complete the promotion without moving trunk again.
- If trunk is C, do not overwrite it. Keep the prepared promotion durable and expose a structured reconciliation conflict containing expected A, target B, and actual C.

C does not discard the proposal. It means the exact A → B operation can no longer be completed automatically. A later judgement process may establish what C represents, derive B' by replaying the proposed change onto accepted C, and prepare a new C → B' promotion.

Reads and inspection remain available in the C case. The repository opens with the conflict exposed; promotion remains paused until reconciliation is resolved.

### Retry semantics

Retrying a prepared or completed promotion returns the originally allocated promotion and successor-intent identities. A compare-and-swap race is reread immediately so B can complete and C can be exposed in the same operation.

The ledger's current intent advances only when promotion completion is durable. If completion succeeds but its acknowledgement is lost, retrying reads the completed promotion and repairs the live repository's cached current intent.

### Agreed ownership rule

> A prepared promotion authorises one exact conditional mutation. Retry it when its precondition still holds, finish it when its target already exists, and preserve rather than overwrite unexpected intent.

## Resolution 008: proposal admission is the stable native API boundary

**Status:** Agreed and implemented for the current slice

### Reservation

The first native API could accidentally make today's approve-all implementation part of the permanent contract by treating `POST /proposals` as “judgement finished and promotion succeeded.” Real judgement will often be asynchronous: it may run tools, amend the proposal, wait for a simulator, or require human attention.

Combining admission and final outcome would force a breaking API change as soon as judgement becomes real. Mixing reconciliation conflicts into the current-intent response would likewise confuse accepted repository content with operational projection state.

### Resolution

The stable API meaning of a successful proposal request is:

> The repository durably admitted this immutable change version.

The initial control API is:

```text
GET  /v1/repos/{repoID}/intent
PUT  /v1/repos/{repoID}/intent        # one-time root-intent bootstrap
POST /v1/repos/{repoID}/proposals
GET  /v1/repos/{repoID}/changes/{changeID}
GET  /v1/repos/{repoID}/changes/{changeID}/versions
```

`POST /proposals` requires a repository-scoped idempotency key and accepts a base intent plus an engine-neutral content reference. It is a command that creates a `Change` and immutable `ChangeVersion`; it does not introduce a separate Proposal resource.

The response always returns the admitted change and version. It may include a completed promotion when the current judgement implementation finishes during the request, but clients must not depend on synchronous promotion. A held or stale-base proposal remains a successful admission.

The current implementation uses an approve-all application service that invokes the separate domain operations `Propose` and `Promote`. HTTP validates and maps the wire contract; it does not own that lifecycle. There is no public promotion endpoint: promotion remains an outcome chosen through repository judgement.

`GET /intent` returns accepted content only. Change inspection returns the latest immutable version and its promotion when completed. Full version history is exposed separately through a bounded, cursor-based endpoint so the change summary cannot grow without limit.

Producer provenance is assigned by the authenticated service boundary, not accepted from proposal JSON. The current control-token slice records `control-api`; richer authenticated actor identity can replace that attribution without changing the proposal wire shape.

### Remaining edges

This resolution does not yet settle:

- the judgement-process read API and event stream;
- repository operational status and reconciliation-conflict endpoints;
- asynchronous retry and wake-up mechanics after admission;
- actor identity beyond the current control API authority;
- native API authorization distinct from the shared control token;
- how a richer client discovers amendments and reconciliation instructions.

### Agreed ownership rule

> Proposal admission is durable and stable; judgement and promotion are evolving consequences that must not be baked into the submission transport.

## Resolution 009: external Git writes cannot move canonical trunk

**Status:** Agreed and implemented for the current slice

### Reservation

The existing smart-HTTP server delegated an authorised write directly to Git receive-pack. A valid repo write token could therefore move the default branch without creating a change version, invoking judgement, recording a promotion, or advancing the intent ledger. Git's transport semantics had become an accidental second authority over canonical intent.

Rejecting that bypass with candidate refs plus a separate proposal request introduces its own risk: temporary Git adapter plumbing could fossilize into the product workflow. Ref names could be mistaken for change identity, ref existence for holding state, or a two-step “push here, then call this API” ritual for the native experience.

New repositories also need one explicit root-of-trust operation before any accepted intent exists to govern proposals.

### Resolution

For the current slice, the external Git adapter configures each receive-pack process to reject updates to the repository's canonical branch. This protection is request-scoped to the external transport. Internal promotion still advances the trunk projection through the prepared compare-and-swap protocol from Resolution 007.

External Git writes may publish content to noncanonical refs such as `refs/candidates/...`. Those refs make Git objects addressable to the engine; they do not create changes, establish identity, represent holding lifecycle, or authorise promotion.

The temporary operational sequence is:

```text
git push <candidate ref>       # transport stores content
propose(baseIntent, contentRef) # native domain admits a change version
judge                          # currently approve-all
promote                        # only this moves canonical trunk
```

The root intent is the one exception because no prior accepted intent exists. A trusted control caller performs an idempotent `PUT /v1/repos/{repoID}/intent` with an already available immutable content reference:

- if trunk is absent, the operation establishes the first intent;
- retrying the same content returns the same intent;
- attempting to bootstrap different content after initialization returns a conflict and cannot move trunk;
- unavailable or engine-incompatible content is rejected.

Bootstrap is administrative root-of-trust establishment, not an alternate promotion endpoint.

### Adapter boundary

Candidate ref names are not change IDs, workstream IDs, or idempotency keys. Their namespace, retention, and eventual removal remain Git-adapter concerns. The native proposal operation accepts an engine-neutral immutable content reference and does not know which candidate ref made that content available.

The native proposal API is durable. Requiring users or clients to coordinate a candidate-ref push with a separate API call is not. A future receive-pack proxy, native client, jj adapter, or other transport may collapse storage and proposal admission into one honest interaction without changing the intent domain.

This slice does not decide whether the eventual Git UX permits typing `git push origin main`, uses a configured submission ref, or requires richer tooling. It establishes the invariant that the requested Git ref cannot itself determine acceptance.

### Remaining edges

- how candidate refs are named, expired, and garbage-collected;
- whether candidate refs should be hidden after admission;
- how a future Git adapter maps one multi-ref push to one or more proposals;
- what exact status an eventual receive-pack proxy reports while judgement is pending;
- how bootstrap authority and provenance become more granular than the current shared control token;
- whether repository creation should eventually establish a server-defined empty root intent instead of using a separate bootstrap operation.

### Agreed ownership rule

> External transports may make content available and request admission. Only repository promotion may advance accepted intent and its trunk projection.

## Resolution 010: submission continues the current workstream

**Status:** Agreed

### Reservation

After submitting a change, a workspace needs a useful base for immediate successor work. Returning to the last accepted intent keeps new work independent, but makes the submitted content disappear from the active workspace while judgement is pending. Continuing from the submitted change preserves the developer's working context, but creates an explicit dependency on unpromoted work.

There was also a question about whether a Git commit already provides the native change boundary.

### Resolution

Submitting B freezes and proposes its current version, then starts successor work C on top of B. This is the default when continuing the same workstream. If B is held, C remains usable and explicitly depends on B.

Starting a new workstream is a separate operation and normally begins from current accepted intent. The client should not ask which behavior is desired after every submission; continuity is the default, while starting independent work is explicit.

Conceptually:

```text
Accepted intent: A
Working change: B based on A

submit B

Submitted change: B
Working change: C based on B
```

If B promotes unchanged, the distinction collapses naturally. If B is amended, rejected, or conflicts, reconciliation must preserve C and explain how its dependency changed rather than discarding it.

### Git compatibility boundary

A Git commit can provide immutable content for a compatibility adapter, but it does not perform the whole native operation. It does not by itself:

- create or preserve logical change identity;
- submit the version for judgement;
- record held or promoted state;
- create and track the successor relationship.

The native target is closer to a continuously snapshotted working change: editing evolves the current change, submission freezes a proposed version, and successor work begins immediately. A Git adapter may initially require a clean commit, but that requirement is adapter plumbing rather than the intended product ergonomics.

### Agreed product rule

> Continue a workstream from what was just proposed; start a new workstream from accepted intent.

### Remaining edges

- how a thin Git client represents the new working change before it has content;
- when the successor receives a durable gitrdone change ID;
- the exact command and UX for starting an independent workstream;
- how clients expose recovery when a dependency is amended or rejected.
