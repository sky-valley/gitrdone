# GitRDone service manual

This is the schema-permissive service manual for the hosted GitRDone service.
It explains the operational promise and how to resolve it. Exocomp does not
execute this prose: [aikea.lock](aikea.lock) is the pinned executable truth.

GitRDone's application configuration and HTTP contract remain authoritative in
the repository [README](../README.md). Exocomp owns only the service-local
checkout, build, immutable installation, systemd definition, and the typed host
requirements declared in the lock.

## Promise

Each GRD node tracks `sky-valley/gitrdone@main`, builds the service, installs the
verified executable into an immutable release, and keeps the stable
`gitrdone.service` unit enabled and active.

The stable unit is intentional. GitRDone owns graceful draining, and systemd
allows 150 seconds so it can exceed the deployed two-minute application drain.
An update is one normal service restart; no overlapping generation handoff is
declared.

## Build

Build from the repository root:

```text
CGO_ENABLED=0 go build -o gitrdone ./cmd/gitrdone
```

The output must be a regular executable named `gitrdone`. Go toolchain
selection and caches are node facts supplied by the resident edge unit.

## Runtime

The process:

- runs as the node-local `gitrdone` user and group;
- receives configuration from the operator-owned
  `/etc/gitrdone/gitrdone.env`;
- starts after and wants `network-online.target`;
- requires `/var/lib/gitrdone` to be mounted;
- restarts with `Restart=always` and `RestartSec=5s`;
- receives `LimitNOFILE=524288` and `TimeoutStopSec=150s`; and
- executes directly from the immutable Exocomp release selected by the current
  activation.

The environment-file path and run-as user are node facts. Secret values never
belong in either Aikea file.

## Host requirements

Before activation:

- the `gitrdone` user and its primary `gitrdone` group must exist;
- `/var/lib/gitrdone` must be the durable mounted volume, not a root-disk
  directory;
- `/etc/gitrdone/gitrdone.env` must be root-owned and mode `0600`;
- `GITRDONE_STORAGE_ROOT` must resolve to `/var/lib/gitrdone`;
- the environment must supply the required Control bearer and each
  environment's address, base URL, database URL and shutdown policy;
- the node must reach its Postgres primary; and
- Caddy, DNS, firewall policy, volume provisioning and database provisioning
  remain operator-owned.

The lock proves the runtime identity and exact mountpoint because Exocomp has
typed checks for them. Environment-file ownership, environment-key
completeness, database reachability and HTTP health remain operator checks; the
lock must not imply that Exocomp verifies checks it cannot execute.

## Environments

The same lock is resolved independently on:

| Environment | Node | Public health endpoint |
|---|---|---|
| local | `gitrdone-local-1` | `https://grd-local.differ.ac/healthz` |
| dev | `gitrdone-dev-1` | `https://grd-dev.differ.ac/healthz` |
| main | `gitrdone-main-1` | `https://grd.differ.ac/healthz` |

Node credentials, environment values, public routes and data remain distinct.
The shared lock does not make one node authoritative over another.

## Verification

For each node, advance to the next environment only after:

1. `gitrdone.service` is active without a restart loop.
2. its `ExecStart` points into the selected immutable Exocomp release.
3. the mounted `/var/lib/gitrdone` volume and its repository data remain
   present.
4. the public health endpoint returns `204`.
5. boot logs show the expected durable Postgres configuration and listener.
6. a second unchanged Exocomp pass performs no build, publication, or systemd
   mutation.

Health and data observations verify the application promise. They do not grant
Exocomp authority over Caddy, DigitalOcean volumes or Postgres.

## Retention and rollback

Exocomp retains the current immutable activation and two predecessors after
completed cleanup. If activation fails before systemd mutation, the running
service is untouched. For a failed first adoption, stop `exocomp-edge`, restore
the captured pre-adoption unit and edge binary, and revert the lock change
before resuming the old edge.

Do not repair immutable releases or activations in place. A new source commit
must produce a new verified candidate.
