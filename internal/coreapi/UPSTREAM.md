# Upstream Core API fixes

This client carries workarounds for bugs/gaps in the control-plane
OpenAPI document and its surface. Each item below should be fixed at the
source (the control-plane service's spec generation / route design);
doing so lets us delete the corresponding workaround here and regenerate
a cleaner client. This file is the running checklist.

When an item is fixed upstream, remove its workaround (cited by file) and
delete the entry.

## 1. Nullable arrays use JSON-Schema type-union shorthand

**Symptom:** slice fields are declared `"type": ["array", "null"]`
(JSON Schema 2020-12). ogen models a schema's `type` as a scalar and
rejects the union form, so generation fails outright.

**Fix upstream:** emit non-nullable arrays — an absent collection
serialises as `[]`, never `null` — i.e. `"type": "array"`.

**Workaround:** `spec/normalize.go` (`collapseTypeUnions`) rewrites the
union to the bare type before generation. Removable once the spec stops
emitting the `null` member.

## 2. Operations declare only 200 but return 201/204

**Symptom:** every operation's spec lists `200`, but the server answers
`201 Created` on POST creates and `204 No Content` on DELETEs. ogen
routes those unenumerated codes to the error decoder and fails to decode
a *successful* response (`unexpected Content-Type: application/json`).

**Fix upstream:** declare each operation's real success code (201 for
POST creates, 204 for DELETEs, 200 for reads).

**Workaround:** `spec/normalize.go` (`collapseResponses`) collapses each
operation to a single `2XX` success + `default` error, so any 2xx
decodes as the success type. Cost: the `2XX` range forces ogen to wrap
results in `*…StatusCode`, which the command fetch closures unwrap via
`.Response`. Declaring the true codes upstream removes the collapse *and*
the wrapper — generated methods return `(*T, error)` directly again and
the closures drop the `.Response` unwrap.

## 3. `mirror remove` rewires to delete-by-coords on next spec refresh

**Resolved upstream** — entiredb refactored the mirror surface (ENT-741,
now on `main`; the failure-message follow-up ENT-743 / entiredb#1830 is
merged). The old repoId-vs-mirrorId gap is gone: the by-mirror lookup
(`GET /repos/by-mirror/{provider}/{owner}/{repo}`) is removed and delete
is now `DELETE /mirrors?provider&owner&repo&clusterHost` — by upstream
coords, no `{mirrorId}` path param. Removal no longer depends on any
id-equality invariant.

**Why this entry still exists:** our committed spec predates the
refactor, so the generated client keeps the old `LookupRepoByMirror` +
`DeleteMirror{MirrorID}` methods and `newRepoMirrorRemoveCmd` still does
the two-call lookup→delete. Unlike #1/#2, the next spec refresh is **not
a free drop** here — it's a required CLI change:

- Drop the `LookupRepoByMirror` call (endpoint gone) and its
  id-equality comment.
- Call `DeleteMirror` once with `{Provider, Owner, Repo, ClusterHost}`
  query coords (mirrors the create shape) instead of a `{mirrorId}` path.
- Handle the new status contract — **404 is now a real, surfaced error,
  no longer swallowed as idempotent success**:
  - 404 — no mirror / not visible to caller / mirrored on another cluster
  - 403 — mirror exists but caller lacks write+ on the upstream
  - 503 — mirror authz service unavailable
  - 401 — caller has no linked GitHub identity
  - 204/200 — removed (concurrent-delete races resolve to 204 server-side,
    so a 404 always means "not removed, here's why")

Wire types that disappear on refresh: `LookupRepoByMirrorInput`,
`MirrorRepoPath`, `LookupRepoByMirrorOutput`. `DeleteMirrorInput` changes
from `{MirrorID path}` to the four query coords above.
