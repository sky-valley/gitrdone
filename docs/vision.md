# gitrdone: Repositories With Judgement

## Status and authority

This is the canonical product vision for gitrdone. Read it before making product or architecture decisions.

The [original handoff](vision-source-handoff.md) is preserved verbatim for context, rationale, rejected ideas, and the path by which the project arrived here. Where that handoff conflicts with this document, this document wins. In particular:

- The existing gitrdone implementation is disposable scaffolding. It is not an architectural constraint.
- Unmodified Git compatibility is highly valuable, but it is no longer a mandatory constraint.
- A custom client or richer protocol is allowed if the native judgement experience requires one.
- Modularity remains a non-negotiable design goal. The judgement system must not become inseparable from Git, jj, GitHub, one model provider, or one CI executor.

This document is a product constitution, not a detailed implementation specification. Settled principles are labelled as such. Open design questions must be worked through without silently weakening the mission.

Material architectural tensions and agreements are tracked in [Reservations and Resolutions](reservations-and-resolutions.md).

## Mission

**Create repositories with judgement as a native capability.**

A gitrdone repository does not merely store versions and move refs. It understands its purpose and the priorities of the people responsible for it. Proposed changes become accepted developer intent through inspection, evidence, judgement, and, when appropriate, amendment.

The concise formulation is:

> A modular repository system in which changes become intent through judgement.

This is not merely adaptive automation on top of Git. It is a different repository model. Git, jj, GitHub, CI systems, and agent runtimes may participate, but none of them defines the product.

## The problem

Today's integration language is composed mostly of branches, pull requests, CODEOWNERS, branch-protection settings, and static CI configuration. These mechanisms force developers to predict integration consequences in advance and express them as exhaustive rules.

That produces uniform ceremony for non-uniform changes. A typo and a data-model migration may receive the same review ritual and consume the same CI budget. Static rules can match files or labels, but they do not exercise judgement about purpose, significance, risk, or intent.

The motivating case is a repository where Noam does not want to block Ion from contributing normally, but does care when Ion changes the data model. Depending on the particular change, the right outcome may be:

- promote immediately;
- promote and notify Noam;
- hold for Noam's review;
- run deeper inspection or simulation first;
- amend the change, then promote it;
- split a mixed change so that independent parts receive different treatment.

The repository should determine that treatment from its purpose, priorities, evidence, and the actual change—not from a static ceremony selected by the contributor.

## The native experience

The central product loop is:

> propose → understand → inspect → amend → promote → reconcile

A developer or agent proposes a change and continues working. The repository evaluates the proposal against current developer intent. It gathers proportionate evidence, decides what consequence is warranted, records why, and either promotes, holds, amends, splits, combines, or rejects the proposal. If the repository evolves the proposal, that evolution flows back into ongoing work through reconciliation.

The consequence of proposing a change is intentionally not encoded by the location to which it was pushed. A proposal does not know in advance whether it deserves immediate promotion, review, heavy CI, amendment, or no ceremony at all.

The experience should feel like working with a capable integration colleague, not submitting paperwork to a rule engine.

## Non-negotiable principles

### 1. Trunk is accepted developer intent

There is one canonical expression of what the responsible developers currently intend the software to be. Trunk is not merely the latest ref that somebody had permission to move.

Nothing becomes canonical intent accidentally. Promotion is an explicit repository outcome, even when it happens immediately and invisibly.

### 2. Judgement is native, not an after-the-fact bot

Judgement owns the transition from proposed change to accepted intent. It is not a comment posted after Git and branch protection have already determined the outcome.

The repository may use agents, deterministic tools, simulations, human review, historical evidence, or combinations of them. Those are arms of the judgement system.

### 3. Different changes deserve different consequences

The system performs both judgement and triage. It decides not just whether a change is acceptable, but what evidence and attention are proportionate to it.

The absence of ceremony is a valid positive outcome. A typo should be able to flow without paying for an agent in a simulator. A data-model change should be able to trigger notification, deeper evidence, or a human gate.

### 4. Push-and-keep-working

Submitting work must not force the developer to wait for integration ceremony. The developer continues from their current intent while the repository resolves the proposal on its side.

If the repository amends or rebases the proposal, reconciliation must preserve what changed, why it changed, and how ongoing dependent work relates to the new version.

### 5. Modularity without lowest-common-denominator semantics

The product must remain modular across VCS engines, transports, model providers, CI executors, review surfaces, and notification systems.

Modularity does not mean pretending every substrate has the same capabilities. Adapters are capability-aware. An engine may expose stable change identity, stored conflicts, descendant rewriting, an operation log, or only commits and refs. The core can use richer capabilities without baking one engine's vocabulary into every domain boundary.

The product integrates the pieces whose joint behaviour defines repository judgement and modularises the commodity mechanisms around them.

The working Christensen/Wardley interpretation is: integrate where the product is not yet good enough and cross-layer performance matters; modularise at boundaries that can be specified honestly; rent commodity mechanics; own the semantics that make repository judgement coherent. Modularity is not permission to outsource the novel experience, and integration is not permission to weld the product permanently to its first substrate.

### 6. Compatibility serves the mission

Git compatibility has enormous value: existing tools, history, credentials, editors, hosting systems, and habits become available with little adoption cost. Preserve it everywhere it does not weaken repository judgement.

Do not weaken the native model merely to make it look like ordinary Git. If full native judgement requires a richer client, protocol, or storage model, that is permitted.

The decision rule is:

> Never weaken repository judgement to preserve Git compatibility. Preserve Git compatibility everywhere it does not weaken repository judgement.

### 7. Consequences are explainable and auditable

Natural-language guidance does not mean unaccountable magic. Every material outcome must retain the governing purpose and priorities, the version judged, the evidence considered, the actions taken, and the rationale.

The repository must be able to answer: what happened, why, under whose authority, against which version of policy, and what changed afterward?

## Native product objects

These are the current conceptual objects. Their exact schemas are not settled, but implementations should preserve the distinctions.

### Repository

The holder of canonical intent, purpose, priorities, capabilities, history, and authority.

### Change

A durable proposed evolution of developer intent. A change survives versions and server-side amendments. It is the natural unit of idempotency and conversation when the substrate can support that identity honestly.

Users should not be forced to manipulate opaque change IDs. IDs are machinery; human names, descriptions, workstreams, and context are the interface.

### Version

One concrete realisation of a change. A bot amendment creates a new version rather than erasing the submitted version. Provenance and authorship must remain visible.

### Workstream

A human-meaningful logical lineage or stack of changes: for example, “the auth refactor.” It is metadata and narrative, not a physical coordination slot.

### Workspace

A physical execution slot for one human or process. Workspaces should be cheap, isolated, and disposable. Multiple agents should not compete inside one working directory.

### Judgement

An auditable interpretation of a particular change version against the repository's purpose and priorities. A judgement may be provisional while evidence is gathered and may be superseded by a later judgement over a new version or base.

### Evidence

The inspections, tests, simulations, analyses, historical facts, human statements, and agent findings used to support a judgement.

### Outcome

The repository action resulting from judgement. Candidate outcomes include:

- promote;
- promote and notify;
- hold for further evidence;
- hold for human judgement;
- amend and reconsider;
- split or combine;
- reject or bounce;
- preserve a conflict as work to be judged and resolved.

### Dependency

A relationship in which one unpromoted change builds upon another. Rewriting or promoting a dependency may require dependent changes to be rebased or reconsidered.

### Reconciliation

The process by which repository-side evolution returns to active work. Reconciliation includes both mechanics and explanation. It is not merely `git pull` and it is not optional polish when the repository can amend proposals.

## Purpose, priorities, and authority

Policy is expressed primarily as what the repository is for and what its responsible developers care about, rather than as an exhaustive list of static conditions.

Purpose and priorities are text files stored and versioned in the repository. Repository skills are also stored with the repository and provide capabilities and operating knowledge to the judgement process.

The governing inputs include:

- a versioned repository purpose;
- versioned priorities and concerns;
- skills and operating knowledge stored with the repository;
- deterministic safety and authority constraints;
- evidence learned from previous judgements.

The exact filenames and conventions remain open, but the authority location is settled: purpose and priorities come from the current accepted repository content.

Natural-language judgement does not eliminate hard authority boundaries. Candidate content must not be able to grant itself permission by editing the instructions that judge the same candidate. A candidate is judged using the purpose, priorities, and skills from current canonical intent. Proposed changes to those files become active only after promotion. Deterministic constraints remain appropriate for authentication, authorisation, resource limits, and fail-safe behaviour.

Agents exercise judgement within granted authority. They do not manufacture their own authority.

## Triage and adaptive CI

The system needs more than one verdict-maker. Triage decides which arms to bring into the process and when enough evidence exists.

Possible arms include:

- fast semantic classification;
- focused deterministic tests;
- static analysis;
- repository-specific skills;
- specialist review agents;
- security analysis;
- simulator or browser execution;
- human review;
- amendment agents;
- post-promotion monitoring.

Adaptive CI means selecting and sequencing these capabilities in response to the significance and uncertainty of the change. It does not mean allowing an agent to waive immutable safety or authority constraints.

Final promotion must be based on the exact integration state being promoted. Evidence gathered against a stale trunk may need to be refreshed. Concurrency mechanics can preserve operations, but they do not themselves prove that independently acceptable changes remain acceptable when combined.

## jj's role

jj is currently the strongest candidate for the first rich VCS engine because its semantics fit the mission:

- change identity distinct from commit identity;
- automatic rebasing of descendants after rewrites;
- first-class storable conflicts;
- an operation log with concurrent-operation handling;
- working-copy state represented as a commit;
- explicit workspaces.

Do not rebuild those semantics merely for ownership's sake if jj can provide them reliably.

At the same time, gitrdone is not “based on jj” as its product identity. The relationship is that gitrdone may embed or operate a jj engine. Judgement remains the novel product capability.

The engine boundary must be modular and capability-aware. gitrdone-level change history, judgement evidence, authority, and rationale may require durable state above the jj operation log. jj should not be forced to own product concepts that are not VCS semantics.

Whether jj runs as an embedded library, an isolated service, or another boundary is an implementation decision to prove, not a positioning commitment.

## Git's role

Git is the preferred compatibility surface where it fits:

- import and export format;
- wire protocol for existing clients;
- bridge to existing repositories and tools;
- possible storage backend;
- adoption path for users who do not need the richest experience.

Plain Git clients may receive a reduced experience if they cannot represent native change identity or reconciliation honestly. That gradient must be explicit rather than hidden behind misleading protocol behaviour.

Open Git questions include:

- how an ordinary push is admitted without prematurely moving canonical trunk;
- what identity survives a client-side amend or rebase;
- how held and amended work appears during fetch and pull;
- whether a one-time refspec configuration, hook, thin wrapper, or richer client is justified;
- how signed commits and authorship survive server-side amendments;
- how much of the portal explanation can be delivered without custom client support.

These questions are important, but Git must adapt to the product model—not define it.

## Holding, amendment, and promotion

Every proposal is logically held until judgement promotes it, even if the judgement is immediate. “Holding bay” describes lifecycle, not a required storage implementation.

The holding model must retain enough information for:

- original submitted versions;
- current versions;
- dependencies;
- conflicts;
- evidence and decisions;
- bot amendments and attribution;
- human interventions;
- supersession and retries;
- promotion against a precise trunk state;
- garbage collection without losing required audit history.

Refs may be one projection of this state. They are not presumed to be the complete storage model.

A server amendment must not silently erase provenance. The developer's submitted version and the bot's transformation are distinct facts even if they belong to the same logical change.

## Workspaces, workstreams, and branches

Branches should not define coordination inside the native model. Their traditional responsibilities separate into:

- workspaces for physical isolation;
- workstreams for human narrative and logical lineage;
- changes for durable identity;
- dependencies for stacking;
- judgement outcomes for integration and review.

Branches may remain part of Git-compatible UX. Saying that branches are dead is an internal coordination claim, not a demand that Git users stop seeing refs immediately.

All active work should normally be understood relative to current canonical intent. Rich engines may keep descendants continuously rebased. Other clients may reconcile less gracefully. Stacking unpromoted changes is first-class: if B depends on A, promotion or amendment of A should propagate through B's known dependency rather than orphaning it.

## Divergences

For Differ, user-specific app variants are called **divergences**, never forks.

Divergences share the same high-level mechanic as repository changes: they evolve on the side and become main developer intent only when evidence and judgement deem them ready. This is a valuable unifying abstraction.

Do not assume that repository changes and runtime app divergences must share every implementation detail. They may have different privacy, deployment, state, rollback, and lifecycle requirements. Share the judgement pattern and product language where it fits; preserve domain boundaries where it does not.

## Known tensions and working hypotheses

These tensions have already been identified. They are design work, not vetoes. The hypotheses are starting points to test rather than settled contracts.

### Change identity across different clients

The native system needs durable change identity. jj can preserve that identity through its own rewrites, but an ordinary Git client does not necessarily transmit the same identity after a local amend or rebase.

The current working hypothesis is that gitrdone assigns authoritative identity when a proposal is admitted. That identity covers the proposal's server-side lineage and versions. A later plain-Git rewrite may be treated as a new proposal unless an adapter can establish continuity honestly through explicit metadata or reliable evidence. jj or a native client can provide the richer continuity experience.

Heuristic similarity may help users recover lineage, but it must not be presented as guaranteed identity.

### Submission versus a Git ref update

In the native model, proposing work and moving canonical trunk are different operations. A normal Git push traditionally asks the server to move a named ref, so the Git adapter must define exactly how that action maps to submission without lying to the client.

Candidate approaches include a configured submission ref, a receive proxy with explicit semantics, a thin client, or a reduced Git-compatible workflow. The right answer is the one that produces the least surprising honest experience while preserving repository judgement. “Push normally” is an experience to prove with client transcripts, not an assumption that gets to distort the native model.

The current implementation uses a reversible reduced-workflow experiment: external Git receive-pack may upload candidate refs but may not update canonical trunk. A trusted one-time bootstrap establishes the root intent; later trunk movement occurs only through promotion. Candidate refs are adapter storage, not changes, holding state, workstreams, or durable identity. This experiment deliberately postpones the final `git push main` UX decision without allowing Git ref semantics to overtake repository judgement.

### Server-side rewriting and local descendants

The repository can only rewrite descendants it knows about. A developer may have unpublished plain-Git work based on an earlier submitted version. That work cannot be transparently auto-rebased by the server.

The native reconciliation model should solve the full case. Git adapters may fall back to an explicit merge or rebase workflow. This is where richer tooling can earn its place. The portal is therefore part of the core product loop whenever the repository can amend work, even if its first rendering is minimal.

### VCS state versus the judgement ledger

Commits, refs, jj operations, and stored conflicts are VCS facts. Purpose snapshots, evidence, decisions, authority, bot rationale, human interventions, and outcome history are judgement facts.

The working hypothesis is a durable judgement ledger above a pluggable VCS engine. Refs may expose or locate held state, and the jj operation log may preserve repository mutation history, but neither should be presumed to replace the product ledger.

### Adaptive guidance versus hard authority

Purpose and priorities should guide contextual judgement, but candidate content is untrusted. A proposal that edits the repository's priorities or agent skills must not thereby change the authority used to judge itself.

The working hypothesis is to evaluate against the purpose, priorities, and skills in current accepted intent. Changes to governing content are themselves proposals and do not govern their own judgement. Authentication, authorisation, resource ceilings, and fail-safe behaviour remain deterministic boundaries around agent discretion.

### Concurrent judgement versus final integration

Several proposals can be inspected concurrently. Their final combination can still behave differently from either proposal evaluated alone.

The working hypothesis is parallel evidence gathering with promotion against an exact canonical base. The coordinator must reconsider or refresh evidence when the integration state changes. A VCS operation log protects mutation history; it does not substitute for semantic integration judgement.

### Amendment versus provenance

A logical change may survive server amendment while its concrete commit identity, signatures, authorship, and contents change.

The working hypothesis is immutable submitted versions plus explicitly attributed transformations and new versions. “Same change” means continuity of intent and conversation, not permission to erase who produced which bytes or why.

## Strategy

GitHub owns a socially entrenched integration surface. A GitHub App that dynamically decides whether to open PRs, request review, run checks, or promote work may be an effective way to prove demand while GitHub still renders the familiar interface.

That parasitic route and the native repository route answer different questions:

- The GitHub route tests whether adaptive judgement delivers value in an existing workflow.
- The native route tests the full repository experience, including implicit holding, durable change identity, server amendment, stacking, and reconciliation.

Either may be a useful first market wedge, but the GitHub constraints must not silently become the native product model. Strategy and architecture should meet through adapters.

## Recommended proof sequence

This sequence is a working recommendation, not a settled roadmap.

### Proof 1: the judgement-native vertical slice

Prove the Ion scenario using whatever controlled interface most clearly expresses the native model:

- establish repository purpose and priorities;
- submit ordinary and data-model changes;
- promote ordinary work with little or no ceremony;
- notify or block on data-model work as priorities require;
- select proportionate evidence;
- amend a proposal and retain both versions and rationale;
- reconcile the result to continuing dependent work.

Do not let Git compatibility obscure whether the judgement loop itself is good.

### Proof 2: engine capability

Prove the selected VCS engine can support durable change identity, versions, dependencies, rewriting, stored conflicts, concurrent operations, restart durability, and exportable repository state.

### Proof 3: Git compatibility gradient

Write executable client transcripts for clone, propose, hold, promote, amend, continue locally, fetch, and reconcile. Establish precisely which guarantees plain Git receives and where richer tooling earns its place.

### Proof 4: policy trust and audit

Attempt to subvert judgement from inside candidate content. Prove that governing policy, agent authority, evidence, and outcomes remain attributable and bounded.

### Proof 5: parasitic market wedge

If useful, map the native outcomes onto GitHub PRs, checks, hidden refs, notifications, and merge controls without confusing that projection for the core domain.

## Ideas rejected unless new evidence appears

### CRDTs at the integration layer

CRDT convergence is not semantic correctness. Automatically converging source text can erase the very conflict signal that should invoke judgement. CRDTs may still fit live, intra-session co-editing before work is snapshotted into a change.

### Session identity

A session can produce several independently promotable changes. It is the wrong idempotency grain.

### Branch names as identity

Names are narrative handles, not durable machine identity. They are manually assigned and do not reliably survive rewriting or parallel work.

### Rebuilding commodity VCS semantics

Do not build a new VCS merely to own mechanics already supplied by a suitable engine. Build new mechanics only where the judgement mission requires semantics that available engines cannot supply.

### Static rules as the product language

Deterministic safety boundaries remain necessary, but exhaustive YAML, CODEOWNERS, and branch-protection checkboxes are not the primary expression of developer purpose and priorities.

### “Meta VCS” positioning

That category describes plumbing rather than user value and invites the wrong comparisons. Prefer “repository with judgement,” “adaptive integration layer,” “intent-mediated integration,” or “adaptive merge coordinator” depending on context.

### Multi-VCS compatibility theatre

Modularity is non-negotiable; advertising a compatibility matrix before it serves the mission is not. Build honest adapter boundaries without spending the product's early life proving substrates nobody needs yet.

## Open design questions

These are work to do, not reasons to dilute the vision:

1. What is the exact native submission interaction?
2. What constitutes a change, a version, and continuity across client-side rewriting?
3. Which state belongs to the VCS engine and which belongs to gitrdone's judgement ledger?
4. How are purpose and priorities authored, versioned, corrected, and protected?
5. How does triage decide which evidence arms to invoke and when to stop?
6. How are concurrent candidate promotions evaluated against the exact resulting trunk?
7. How are amendments attributed, signed, explained, and reconciled?
8. What does a native portal or client need to show?
9. What honest subset of the experience can an unmodified Git client receive?
10. How do workstreams and dependencies appear to humans without exposing machinery-heavy IDs?
11. What history must remain forever, and what held state can be garbage-collected?
12. Which abstractions genuinely span repository changes and Differ divergences?

## Positioning and vocabulary

Lead with the pain and the new capability, not the embedded mechanism.

- Primary category: **repository with judgement**.
- Other useful descriptions: **adaptive integration layer**, **intent-mediated integration**, **adaptive merge coordinator**.
- Positioning line: **jj gave commits identity; this gives the repo judgement.**
- Say gitrdone **embeds** or **uses** jj; do not make jj the product identity.
- Use **divergence**, never **fork**, for user-specific Differ variants.
- Treat branches, PRs, refs, commits, and pipelines as mechanisms or projections rather than the native product vocabulary.

## How agents should work on this vision

Act like a colleague helping work out a difficult product, not a gatekeeper reviewing it from outside.

- Protect the mission and modularity.
- Challenge contradictions directly, without performative agreement.
- Bring candidate resolutions and trade-offs with every material objection.
- Distinguish impossible combinations from details that simply need a precise contract or prototype.
- Do not demand mature operational answers before the native experience has been proved.
- Do not let an attractive mechanism—Git, jj, agents, CRDTs, GitHub, or otherwise—quietly become the mission.
- Do not treat the current implementation as a constraint unless explicitly asked to evolve it incrementally.
- Preserve settled rationale and flag concrete contradictions rather than silently deviating.

The recurring question is:

> Does this help a repository turn proposed change into accepted intent through better judgement, while keeping the system modular enough to evolve?
