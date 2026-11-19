# desired.json renderer

`internal/render` turns the apps placed on one node into nibrunner's whole
`desired.json` (nibrunner v2026.11.12). It is pure (standard library only, no
clock, files or network) and the node agent (`drop agent`, M9) is its only
caller. The agent fills `render.Input` from the server's `drop.desired/v1`
payload after staging each release and resolving its secrets; the input types
are the contract and do not import Drop's models.

```go
out, err := render.Render(render.Input{HostID, DesiredSeq, Apps, Keep, Previous, Limits})
// out.Document, out.Revision, out.ContentHash, out.Rejected, out.Tombstoned
```

## Rules

- **Drop owns the whole document** (decision 12). Any appId not starting with
  `d-` in `Apps`, `Keep` or the previous document refuses the whole input with
  `foreign_entries` and lists the ids; nothing is rendered. `ForeignAppIDs`
  gives the same list for `drop agent check` and agent start. The operator
  removes foreign entries; Drop never deletes what it did not create.
- **Never `absent`.** An app in the previous document that the input no longer
  names becomes a stopped tombstone (same deployment, no activation) with its
  volume present. A previous document holding an absent entry, a checkpoint,
  an export or a field Drop never writes is refused.
- **Bad apps fail alone** (`Rejected`, with field names, never values): the
  previous entry is kept unchanged, or the app is left out. `Keep` carries an
  app's previous entry over unchanged, for failed staging or secret
  resolution. A placed volume cannot change size. A malformed input (host id,
  limits, duplicate ids or hostnames) refuses the whole document.
- **Validation (plan 7.3)** runs on the node: appId `^d-[a-z0-9]{15}$`,
  deploymentId `<appId>-dep-<1–9 digits>`, volumeId `<appId>-vol`, sha256 and
  `s3Key = releases/<sha256>`, executable name, SSM parameter
  `<ssmPrefix>/apps/<appId without d->/<NAME>`, hostnames under the allowed
  suffix, env and secret names `^[A-Z][A-Z0-9_]{0,63}$` (not `HOME`, `PATH`,
  `NIBRUN_*`, `DROP_*`, at most 64 entries, values at most 4 KiB, no unknown
  `${NIBRUN_*}` reference), resources within the data model's ranges and the
  node's allocatable, idle timeout 60000–86400000 ms.
- **Rendering:** one executable layer at `/app/<executableName>` whose
  `objectKey` is the sha256 (the agent stages by digest); `workingDirectory`
  `/app`; environment = env + secrets + `NIBRUN_HOSTNAME=${NIBRUN_HOSTNAME}`,
  `NIBRUN_DATA_DIR=/app/data`, `HOME=/app/data`; on-request apps sleep through
  `activation.sleepWhen` `traffic-idle`; public access; Drop's fixed health
  timings and restart policy (crew-dev's values).
- **Canonical JSON:** keys sorted, two-space indentation, trailing newline, no
  HTML escaping; instances and volumes sorted by appId, hostnames by name.
  `revision` is `drop-<desiredSeq>-<12 hex>` of `ContentHash`, the sha256 of
  the compact canonical document without its revision. The agent skips the
  write when `ContentHash(current file)` equals `out.ContentHash`.
- Every rendered document is checked against the vendored schema
  (`ValidateSchema`) before it is returned.

## Vendored schema

`internal/render/schema/desired.schema.json` is nibrunner's
`crates/protocol/schema/desired.schema.json` at tag `v2026.11.12` (commit
`e7986824ed5abdb738364667456b5a24df5f6d62`), byte for byte; nibrun's
`desired.gen.ts` is generated from it. `ValidateSchema` is a focused JSON Schema
2020-12 evaluator for exactly the keywords that file uses; it refuses a schema
with any other keyword, and runs the one lookahead pattern (guest paths) as
hand-written code. To bump nibrunner: copy the new file, update the digest in
`TestVendoredSchemaDigest`, and run the package tests.

## Golden files

`testdata/crew-dev.input.json` renders `testdata/crew-dev.golden.json`. They
come from the sanitized copy of the live crew-dev entry (Crew task M8),
renamed to `d-crewdev00000001`; `testdata/crew-dev.live.json` is that entry,
and a test checks the golden differs from it only in the ids and the
`objectKey`. Secrets are placeholders. Regenerate after an intended change with
`go test ./internal/render -run TestGolden -update` and review the diff.
