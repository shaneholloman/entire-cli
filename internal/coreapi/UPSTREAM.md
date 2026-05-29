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

## 3. by-mirror lookup returns `repoId`, but delete takes `mirrorId`

**Symptom:** `GET /repos/by-mirror/{provider}/{owner}/{repo}` returns a
field named `repoId`, while `DELETE /mirrors/{mirrorId}` takes a
`mirrorId`. `entire repo mirror remove` must feed the lookup's `repoId`
to delete. The two are the same ULID server-side (both the
`mirror_repos` row id — verified live: by-mirror `repoId` == list
`mirrorId` for the same repo), so it's correct, but the client contract
reads mismatched and there's no delete-by-coords route to avoid the
lookup.

**Fix upstream (either):** name the by-mirror response field `mirrorId`,
or add a delete-by-coords route so removal doesn't depend on the
id-equality invariant.

**Workaround:** `cmd/entire/cli/repo_mirror.go` (`newRepoMirrorRemoveCmd`)
passes `repoId` to `DeleteMirror` with a comment explaining the
invariant. No code change needed when fixed — just drop the comment.
