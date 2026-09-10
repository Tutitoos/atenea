# ATENEA remote v1 contract

This directory is the canonical wire contract for `atenea.remote.v1`. It
describes protocol data only. The assets do not claim that a coordinator,
WSS server, enrollment flow, native agent, device, installer, deployment, or
Computer Use execution exists or has run.

## Version and WebSocket negotiation

The exact WSS subprotocol token is:

```text
atenea.remote.v1
```

The current wire version is `1.0.0`. A compatible WebSocket peer must offer
and accept that exact subprotocol and must validate the envelope fields
`protocol: "atenea.remote.v1"` and `version: "1.0.0"`. This is a closed v1
contract; a different token or version is unsupported.

The JSON negotiation payload has three closed forms:

- an agent offer/hello with `agent_version`, `platform`, `architecture`,
  explicit `supported_versions`, `modes`, and `capabilities`;
- a coordinator accept with one `accepted_version`, one `accepted_mode`,
  explicit `grants`, and heartbeat/liveness bounds; or
- a coordinator reject with a typed rejection code and message.

The accepted version must be an element of the prior offer. The accepted mode
and every granted capability must be the appropriate subset/intersection of
the prior offer; a peer must not infer compatibility from array position,
similar names, or omitted fields. These cross-message checks are stateful
runtime rules, not properties established by one JSON Schema instance.

Unsupported versions, modes, or capabilities fail closed: reject or refuse
the negotiation/request, do not dispatch it, and do not silently downgrade or
fall back. The schemas provide the closed rejection/error vocabulary,
including `unsupported_version`, `missing_capability`, `capability_denied`,
and `invalid_envelope`; they do not implement the runtime decision.

## Canonical assets and fixtures

The canonical assets are the nine Draft 2020-12 schemas in this directory:

| Asset | Role |
| --- | --- |
| `definitions.schema.json` | shared identifiers, bounds, capabilities, grants, privacy, and typed data definitions |
| `envelope.schema.json` | closed top-level envelope and message-type/payload binding |
| `negotiation.schema.json` | offer, accept, and reject payloads |
| `heartbeat.schema.json` | ping/pong and liveness metadata |
| `request.schema.json` | granted `remote.desktop.*` requests and safety gates |
| `result.schema.json` | typed successful results |
| `error.schema.json` | typed errors/refusals and their fixed mappings |
| `event.schema.json` | unsolicited session events |
| `binary-frame.schema.json` | standalone binary-frame metadata |

`embed.go` exposes these schemas and the recursive fixture tree through the
embedded `protocolv1.FS`; consumers must not depend on the process working
directory to locate them.

Fixtures are organized as:

```text
fixtures/
  valid/*.json
  invalid/
    malformed_json/*.json
    duplicate_key/*.json
    trailing_json/*.json
    non_object/*.json
    schema/*.json
  embed-probe/non-json.txt
```

The current corpus contains 68 valid fixtures and 86 invalid fixtures. The
invalid validation inventory covers parser/resource failures and schema
counterexamples; the Go tests keep its category and witness inventory closed,
deterministic, and tied to the schemas.

## Wire shape, correlation, and payloads

Every message is a JSON object with a closed envelope containing:
`protocol`, `version`, `message_type`, `session_id`, `device_id`, `sequence`,
`sent_at`, and `payload`. `session_id` and `device_id` are required bounded
identities (1--128 characters using the contract identifier pattern).
`sequence` is an unsigned bounded integer (`0..4294967295`) and `sent_at` is
bounded Unix epoch milliseconds (`0..253402300799999`).

`request_id` is the sole request correlation value and is never repeated in a
payload. It is required on `request`, `result`, `error`, and `binary_frame`
messages, and forbidden on `negotiation`, `heartbeat`, and unsolicited
`event` messages. It is a bounded identifier of 1--128 characters. The
envelope's session/device identity and sequence provide the other wire-level
correlation context; binding them to live state is a runtime responsibility.

JSON is the control representation. A `binary_frame` payload is metadata for
one complete associated binary payload, not the bytes themselves. Its closed
metadata contains `binary_id`, `frame_id`, `purpose: "screenshot"`, an image
content type (`image/png` or `image/jpeg`), `size_bytes`, a 64-hex-character
`sha256`, and privacy metadata. The descriptor deliberately has no nested
`request_id`; the enclosing envelope is authoritative. These assets validate
the hash format and declared size, but matching the hash and size to received
bytes requires later runtime enforcement.

Requests carry an explicit capability, mode, authorization grant, timeout,
and capability-specific arguments. Direct-input `click`, `type`, and `key`
requests in `attended` or `isolated` mode carry an input observation proof and
their matching closed safety gate. Other requests in those modes do not
implicitly carry a safety gate. Text input is bounded to 4096 characters; the
general safe scalar values are bounded by the shared definitions.

These schema and fixture assets implement no storage. Every screenshot
`binary_frame` metadata instance explicitly declares one of the schema's two
privacy states: ephemeral/no persistence (`ephemeral: true`,
`artifact_persistence_authorized: false`) or non-ephemeral with explicit
artifact-persistence authorization (`ephemeral: false`,
`artifact_persistence_authorized: true`). The `retention_ms` value is metadata
only. JSON Schema `default` annotations do not supply omitted values; these
privacy fields are required, and the validator does not materialize defaults.

The assets do not perform authorization, retention, storage, or deletion.
Actual authorization, retention, storage, and deletion are later runtime
obligations. Text input is modeled only as bounded wire data; text persistence
is outside the modeled v1 schema, and project policy is no persistence by
default.

## Bounds and closed validation

All schema objects reject unknown properties. Important contract limits include:

- one control JSON message is at most 16 MiB in the Go validator;
- JSON nesting is limited to 64 containers, and numeric token, significand,
  and exponent resources are bounded before schema validation;
- identifiers are generally 1--128 characters; capabilities are a unique
  list of at most 17, modes at most 3, and offered versions at most 4;
- request timeout is `1..30000` ms, heartbeat interval `1000..15000` ms,
  liveness deadline `1000..45000` ms, grant validity `1..30000` ms, and
  lease `1..5000` ms;
- a binary payload is `1..16777216` bytes, and privacy retention is
  `1..30000` ms.

These are wire bounds, not proof of freshness, authorization, or availability.

## Deterministic Go validation

Run from the repository root. These commands compile the canonical embedded
assets and exercise the fixture inventories. With the required Go modules
already available in local caches, the validation itself does not intentionally
contact external systems; on a cold cache, Go may consult its configured module
proxy while resolving or downloading dependencies.

```sh
go test -count=1 ./pkg/remoteprotocol
go test -race -count=1 ./pkg/remoteprotocol
go vet ./...
go test -race -count=1 ./...
```

The focused package tests validate the valid and invalid corpora, schema
isolation witnesses, embedded-resource loading, parser boundaries, duplicate
keys, trailing JSON, numeric/resource limits, and concurrent validator use.

## Schema evidence versus runtime enforcement

The schemas and tests establish closed JSON shape, discriminator bindings,
field bounds, local syntax/resource rejection, and fixture membership. They do
not establish state across messages or external state. Later runtime
enforcement is required for freshness, ordering across messages, negotiation
subset equality, revocation, authorization and grant lifetime, liveness,
session/device binding, and equality of a declared binary content hash to the
received bytes. A timestamp being in range is not proof that it is current;
an accepted capability field is not proof that it was offered or authorized.

## Evolution policy

All additions must be represented by new closed-schema assets and explicitly
negotiated. Update the version/capability offer and acceptance rules together
with the new assets and fixtures. Unknown fields, capabilities, and versions
remain rejected; there is no silent fallback or implicit interpretation.

Breaking wire changes require a new negotiated version and a corresponding
subprotocol policy. Existing v1 peers must continue to reject that version
unless they explicitly support and negotiate the new contract.
