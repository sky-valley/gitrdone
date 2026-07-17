# Original gitrdone vision handoff

This document preserves the initial handoff verbatim for historical context. It is not the current authority where later decisions differ. Read [vision.md](vision.md) first.

---

# gitrdone — Handoff Brief for Coding Agent

You are picking up a project called **gitrdone**. it's currently a dumb git server. but what we gonna make it: an adaptive integration layer on top of git. This document contains the full context, every decision made so far with its rationale, every idea that was tested and rejected (and why), and the open problems. Read all of it before writing code. The decisions below are settled unless you find a concrete technical contradiction — if you do, flag it rather than silently deviating.

## The problem

Git + GitHub's integration model is static and dumb. Branch protection rules, CODEOWNERS files, and YAML CI pipelines are the language in which developer intent is currently expressed, and it's the worst possible language: rigid, detail-heavy, and incapable of judgement. PRs force uniform ceremony on non-uniform changes — a typo fix and a data-model change get the same review ritual and the same CI bill. Nobody wants to review everything, and fully automatic merging is too dumb. The integration layer needs judgement and triage, not more rules.

The specific motivating scenario: a monorepo where the owner (Noam) does NOT want to block his engineer (Ion) from pushing, but DOES want to be notified whenever Ion touches the data model — or, for some categories, have data-model changes block on his review while everything else is free game. Pipelines should run conditionally on significance: a typo push should not trigger a full CI pipeline with an agent in a simulator.

## The thesis

**Trunk = developer intent.** There is one canonical trunk and it represents what the developer(s) actually intend the software to be. Nothing enters it willy-nilly, but the gate is not a static rule engine — it is a judgement engine.

**Every push enters a holding bay implicitly.** When you push, you do not know the outcome in advance. The change may go straight to trunk, may sit in the holding bay pending inspection/review, or may be amended by a bot (e.g., a PR bot finds bugs and fixes them server-side). Consequences are emergent, decided by agents guided by declared priorities — not promised by a location you pushed to.

**Push-and-keep-working, updates flow back.** After pushing, the developer continues working on their own version. If the system changed what they pushed (bug fixes, amendments), they receive those updates on their next sync (the "portal" moment). Until then the system considers the change resolved on its side.

**Policy = "what you care about," not static rules.** The judgement engine is guided by declared purposes and priorities expressed as guidance (roughly natural language), not exhaustive static configuration. It is bootstrapped with the skills kept in the repo — everyone keeps agent skills in the repo now, so they're available to all agents working on it. The two governing inputs are: purpose (what this repo is for) and priorities (what you care about). From these, the system decides when to open a PR, when not to, what needs review, what doesn't, and which pipelines run under which conditions.

**Adaptive CI.** Think of the raw capabilities of GitHub Actions, but instead of YAML, an adaptive CI guided by purpose and priorities. Judgement AND triage — more arms in the mix, not just a single verdict-maker.

**Divergences follow the same mechanic.** (Context: this connects to Differ, the founder's main product — user-specific app variants called "divergences," never "forks.") Divergences happen on the side and are merged to the main developer intent only when deemed ready, and "deemed" is decided by agents + dynamic CI. The repo-level system and the app-level system are the same machine pointed at different layers: divergences held aside, promoted to intent by judgement.

**Positioning one-liner:** jj gave commits identity; this gives the repo judgement.

## Architecture decisions (settled)

### 1. Embed jj-core as the server's brain; speak git wire protocol to clients

The stack: jj-core (Rust library, deliberately built to be embedded without its CLI — this is how Google wires it into their internal stack) as the server's internal model; git wire protocol as the external interface; the policy/judgement engine on top. Do not rebuild jj semantics — embed the library. The VCS semantics are commodity; the only novel component is the policy layer. Build exactly one component, rent the rest (Wardley logic).

jj primitives this design depends on:

**Change identity separate from commit identity.** A change has a stable ID that survives rewrites; the commit hash is just the current physical snapshot. When the server bot fixes a bug in a pushed change, it's the same change, new version — not an orphaned commit and a rebase apology. Change ID is the thread that makes "you'll receive updates to what you pushed" a first-class operation. **Change ID is also the idempotency key for the whole system** (see rejected ideas for why session and branch name were both wrong).

**Automatic descendant rebasing.** Rewrite a commit and everything built on it rebases automatically. This is the mechanic for (a) keeping live work based on current trunk continuously, and (b) propagating bot rewrites in the holding bay to dependent changes.

**First-class storable conflicts.** Conflicts are stored in commits, not splattered across a working tree as an error state. The holding bay can hold conflicted states as legitimate objects while agents work on them. Critically: **conflicts are the input to the judgement agents**, not a synchronous emergency.

**Operation log with concurrent-op merging.** Every repo mutation is versioned and undoable; concurrent operations are merged rather than rejected. Multiple agents + a human mutating the repo simultaneously is jj's native concurrency model.

**The working copy is a commit.** No staging area, no uncommitted state. Everything is always versioned; push becomes purely a visibility/sync event, not a ceremony. This matches the thesis directly.

### 2. No custom client — mandatory constraint

Plain git clients must work end to end: push with git, get promoted/held/amended by the policy layer, pull the reconciliation with git. jj clients get a richer experience (change IDs surface natively, reconciliation is more graceful) — an upgrade gradient, not a conversion cliff. Adoption friction must live entirely server-side; "point your remote at us" is the entire ask. Gerrit is the existence proof that change-based integration needs at most a commit-msg hook client-side; jj-style snapshot-time identity removes even that.

The one place a thin client wrapper may eventually earn its place: the "portal" UX — surfacing *what the bot changed and why* on sync, rather than a silent rebase. That's a fetch hook + summary, a UX layer, not a protocol requirement. Do not build it first.

### 3. Server shape: two viable implementations, in order

**(a) Cheap prototype: GitHub App via webhooks.** Pushes land on personal refs; the bot manages promotion, opens/manages PRs, uses hidden refs as the holding bay. Nobody changes their remote URL. This is the parasitism phase (see strategy below).

**(b) Honest version: proxy in front of `git receive-pack`.** Own the push moment directly — that's where outcome-branching happens (straight to trunk vs. holding bay vs. bounce-with-fixes). Git remains dumb storage behind the proxy.

The holding bay is just refs either way (e.g., `refs/holding/<user>/<change-id>`). No new storage model.

### 4. Branches are dead as coordination; two concepts replace them

A git branch today does four jobs: names a line of work, isolates it from trunk, serves as the PR unit, and acts as the idempotency key. In this design: isolation is universal (everything is held until promoted), the review unit is decided dynamically by the bot, and idempotency is the change ID. So branches only survive for naming — and naming splits into two separated concepts:

**Workspaces = physical execution slots.** jj workspaces (the worktree equivalent), but cheap and disposable because each workspace's working copy is a commit — zero unversioned state, ever. One process, one workspace, always. Per-agent, per-session, throwaway. Multiple agents in one working directory stops being a problem to solve and becomes a thing you simply don't do.

**Workstreams = logical lineages, pure metadata.** "The auth refactor" is a named stack of changes, not a place. Switching workstreams within one workspace = repointing the working copy at a different change (jj `edit`/`new`), no stash, no checkout anxiety. The workstream name is a narrative handle for humans and an input to the policy layer ("this is data-model work" → Noam reviews); it coordinates nothing physically. Intent metadata is mostly inferred by the bot, not manually assigned.

Workspaces and workstreams recombine freely: three agents on three features = three workspaces, three working-copy changes, three streams. A human joining one = pointing their workspace at that stream's tip.

**All workspaces base on trunk by default**, with auto-rebase continuously pulling promoted trunk forward underneath live work (kills stale-branch rot — divergence is always measured against current intent). Stacking is first-class: a workstream may base on trunk + specific unpromoted changes from the bay (feature B on unpromoted feature A). When A is promoted or rewritten in the bay, B auto-rebases onto the new A — the "updates to what you pushed" mechanic extended to dependents.

**UX warning:** never force users to think in change IDs. That's Gerrit's UX, and Gerrit's UX is why everyone outside Google uses PRs despite PRs being worse. Change IDs are machinery; names are the interface.

### 5. Strategy: parasitism before replacement

The displacement target is not git the tool — git is kept as the wire. The displacement target is **GitHub the integration surface** (PRs, checks, branch protection — where review happens), which is a network-effects product with social gravity. History's warning is Gerrit: technically superior integration model, survives only where an institution imposes it. The escape: start as a layer that *drives* GitHub — the bot manages PRs, the holding bay is hidden refs, the policy engine decides what becomes a PR at all — delivering value while GitHub still renders the UI. Become the surface only after the judgement layer has proven that's where the intelligence lives. (Same wedge logic as Differ's self-healing wedge: enter through the pain, not through the platform swap.)

### 6. Classification and naming

Do NOT call this a "meta VCS." Rationale: (a) meta-VCS historically means tools abstracting over multiple VCSs or managing many repos (Google's `repo`, git-subrepo) — plumbing categories, wrong comparisons, wrong fight; (b) it names the architecture, not the value — nobody adopts this because it wraps three VCSs, they adopt it because PRs suck and branch protection is too dumb. Honest classification: **VCS-agnostic adaptive integration layer** / "intent-mediated integration" / "adaptive merge coordinator." Closest existing reference class: merge queues (GitHub merge queue, Meta's land system, Gerrit submit rules) — but those are static rule engines and this is a judgement engine.

VCS-agnosticism (could wrap git, jj, or hg) is a **private design constraint**, not a shipped feature: keep the policy layer reading only "commits, refs, push events" and never leak git-isms into it — but do not advertise multi-VCS support. Pre-product, a compatibility matrix is cave work. Git is the assumed default; jj/hg support stays latent. Note the asymmetry: git and hg are substrates you'd wrap; jj is different — its rewriting model is a *capability the server needs*, not a substrate. Hence: jj-like semantics inside the server, git-compatible wire outside.

## Ideas tested and rejected — do not revisit without new evidence

**CRDTs / Automerge replacing git diffs at the trunk/integration layer.** Rejected. CRDTs guarantee *convergence*, not *correctness* — Automerge will happily merge two edits into a file that doesn't parse. Converged-and-broken is worse than a conflict, because a conflict at least signals that judgement is needed. Deeper architectural contradiction: the entire thesis is that merging requires judgement; CRDTs are a machine for making integration decisions *without* judgement, deterministically, at the type level. Adopting them at trunk means the data structure pre-empts the bot — building an elaborate judgement engine and letting `automerge.merge()` overrule it. jj's storable conflicts are the *input signal* the agents run on; CRDTs would erase that signal. Supporting evidence: Pijul and Darcs went down the smarter-merge-algebra road with genuinely better theory and won approximately nobody (git-compatibility beats merge elegance); Kleppmann himself regards CRDTs for code collaboration at the version-control layer as unsolved research (syntax trees and intent don't survive character-level merging). **Carve-out that survives:** CRDTs are fine for the live intra-session co-editing layer — two agents (or human + agent) co-editing within one divergence in real time, low-stakes, high-frequency, snapshotted into a change before it faces the policy layer. Three layers, three consistency models: CRDT inside the session, jj-style changes between sessions, judgement at the trunk.

**Session as the idempotency key.** Rejected. Wrong grain: sessions are ephemeral, and one session routinely produces multiple independently-promotable changes. The typo fix and the data-model change from the same sitting must not share a fate — one is free game, one blocks on review. Idempotency lives on the change ID.

**Branch name as the idempotency key.** Rejected. Manually assigned — humans forget, agents collide, and the name encodes nothing the policy layer can use.

**Custom client / new wire protocol.** Rejected as a requirement (see decision 2). Client work is at most a later UX layer for the portal moment.

**Building a new VCS or rewriting git.** Rejected. All VCS semantics needed already exist in jj-core; everything below the policy layer is commodity.

**Static rule expression (YAML, CODEOWNERS, branch protection checkboxes).** Rejected — this is the incumbent being replaced. Policy is guidance ("what you care about"), interpreted by agents, not exhaustively enumerated rules with strong static details.

**"Meta VCS" as the category.** Rejected for positioning (see decision 6).

**Advertised multi-VCS support at launch.** Rejected — compatibility matrix is pre-product cave work. Kept as a latent design constraint only.

## Open problems (unsolved — your judgement calls, flag decisions)

1. **Rebase-reconciliation semantics** when the bot rewrites pushed changes that the user has kept working on top of locally with a plain git client (no jj auto-rebase on their side). jj semantics solve this server-side; the git-client sync path needs careful design. This is the hardest technical problem in the system.
2. **Holding bay state complexity** — lifecycle of a held change (held → inspected → amended → promoted / bounced), what dependents see at each stage, garbage collection of dead changes.
3. **Policy expression format** — how "purpose + what you care about" is actually stored and versioned (in-repo file? conversational? both?), how it bootstraps from repo skills, and how the bot's inferences are auditable.
4. **Triage architecture** — the "more arms in the mix" requirement: multiple agents for triage vs. judgement, how significance is scored (typo vs. data-model change), which pipelines run when.
5. **The portal UX** — what the user sees on sync when their pushed change was amended. Deferred, but the data model should preserve everything needed to render it (diffs between change versions, bot rationale).

## Suggested MVP path (opinion, not settled)

Phase 1: augment gitrdone with light server changes that are still githeavy. as if we were using github apps and webhooks, only no ceremony because it's our server. changes land on per-user refs; a policy agent (guided by a plain-language priorities file in the repo) decides per-change: promote to main / hold + notify / hold + require review / amend then promote. Prove the Ion scenario end to end: data-model touches notify or block, everything else flows, typo pushes skip heavy CI.

Phase 2: introduce jj-core server-side for change identity and bot amendments with dependent auto-rebase; holding bay as real refs; reconciliation path for plain-git clients.

Phase 3: receive-pack proxy; become the push surface.

## Vocabulary and voice notes

Use "divergences," never "forks," for user-specific variants (fork carries Git connotations and the wrong mental model). "gitrdone" is the working project name. When positioning externally, lead with the pain (PRs suck, static rules are dumb) not the mechanism; the jj relationship is "embeds jj-core," never "based on jj" as identity.

---------
