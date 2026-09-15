# Protocol and coding-continuity validation

Measured locally on Windows in September 2026 using Go 1.26.6, Python 3.12,
OpenCode 1.17.11, Codex 0.154.0, OpenAI SDK 3.13.0 and Anthropic SDK 1.5.0.
Live requests used `gpt-5.6-reasoning` through Microsoft 365 Copilot.

## Final candidate: repeated native-client runs

Tested binary SHA-256:
`499e5b31007ddb90e433437b1150b6f21ed655be7fea3394e45fc32e3e5abde9`.

Every run created a fresh, isolated project, sent three additional requirements
and **“Are you done yet?”** during the initial failing check, and required the
client to implement and verify the work. The harness then checked the acceptance
files were unchanged and independently reran all 20 checks.

| Run | Client | Interruption / compaction / bridge restart | Seconds | Client check runs | Result |
|-----|--------|--------------------------------------------|---------|-------------------|--------|
| `protocol-kanban-opencode-03` | OpenCode | All three, verified summary | 189.11 | 2 | 20/20 + independent pass |
| `protocol-kanban-opencode-04` | OpenCode | Ordinary continuation | 214.44 | 3 | 20/20 + independent pass |
| `protocol-kanban-codex-03` | Codex | All three, verified context-compaction event | 311.12 | 4 | 20/20 + independent pass |
| `protocol-kanban-codex-04` | Codex | Ordinary continuation | 214.64 | 3 | 20/20 + independent pass |

OpenCode recorded only the intentionally induced `MessageAbortedError` in the
interrupted run, and no assistant errors in the ordinary run. It issued five and
six `todowrite` calls respectively; each read all six project files. Native clients
owned the tool execution throughout. The candidate's encrypted state directory
was isolated per run; client configuration overrides were process/thread-local.

### What the Kanban checks cover

`tests/kanban_fixture.py` creates starter `service.py`, `storage.py`, `web.py`,
an immutable `checks/test_kanban.py` and a check runner. The implementation uses
Python's standard library: SQLite storage and a WSGI JSON API.

The **17 service tests** cover board/card creation and listing, blank names/titles,
unknown boards, cross-board read/move isolation, reopening the database, move
version increments, invalid-column rollback, atomic WIP-limit rejection (including
unchanged audit/version), released WIP capacity, invalid WIP limits, dense reorder,
invalid position rollback, stale move rollback, stale writes from a second
connection, rename validation/versioning, and isolated persisted audit history.

The **3 WSGI tests** cover HTTP create/list/move, validation/not-found mapping
(`400`/`404`), and stale-version conflict mapping (`409`). WSGI tests exercise the
application interface directly without opening a network listener.

## Failures found during development

- SDK stream accumulation failed when `response.created` omitted `output: []`.
- Compaction had the wrong object type and omitted usage-detail objects.
- Late reasoning could collide with an already-open message's output index.
- An early OpenCode run passed the project checks but ended with `task_incomplete`;
  the harness now rejects unexpected assistant errors rather than counting only
  file correctness.
- A compaction/restart run exposed lost parallel tool results on reused Microsoft
  conversations: six client reads succeeded, but only the final empty file reached
  the next upstream turn. Chat/Anthropic now use the same complete canonical request
  boundary as Responses, with execution evidence preserved separately. A regression
  test exercises the actual `includeHistory=false` payload path for all three formats.

Earlier exploratory runs are not included in the final-candidate table above.

## Deterministic protocol and recovery checks

`TestOfficialSDKContracts` runs **19 official-SDK contracts** against real bridge
HTTP/SSE handlers with a deterministic Microsoft backend. Coverage includes UTF-8
text, Chat/Anthropic streams, Responses lifecycle and sequence numbers, late
reasoning indexes, tools and argument deltas, client-supplied tool results, reused
model call IDs, previous-ID restoration, credential isolation, retrieval/deletion,
`store:false`, empty probes, incomplete output, standalone/in-band compaction,
pending calls and queued tasks after failures/compaction, truncated compaction,
and connection closure after partial output.

Focused Go tests also cover encrypted-at-rest content, restart restoration,
concurrent checkpoint updates, cancellation/replay, reopening new work, capsule
tampering/owner/expiry, closed goals across repeated compaction, session reset and
legacy isolation, operator retention opt-out, private error redaction, and HTTP
disconnect cancellation reaching the buffered/streaming upstream operation.
The full Go suite and Windows race suite passed with SDK contracts enabled.

## Integration checks

- All six live protocol checks passed: Chat Completions, Anthropic Messages and
  Responses, each buffered and streaming, forwarded a fresh read after an edit.
- Image generation returned a decodable **1254 × 1254 PNG**; a separate Responses
  request correctly identified the red apple from an image-bearing tool result.
- Windows installation tests passed for a fresh directory with spaces, matching
  text/image/browser-login aliases, data-preserving upgrade, rejecting a stock
  candidate and leaving services stopped with `-NoStart`. The installer rebuilt
  the exact same SHA-256 as the native-tested candidate.
- Vet, Staticcheck, modernize and the complexity gate (all functions ≤10) passed.

Local reports: `protocol-api-01`, `protocol-images-01`, `protocol-inspection-01`,
and `protocol-installation-01`, each containing `report.json`.

## Reproduce

```powershell
python -m venv .sdk-venv
.sdk-venv\Scripts\python -m pip install -r tests/sdk-requirements.txt
$env:M365_SDK_PYTHON = (Resolve-Path .sdk-venv\Scripts\python.exe).Path
go test ./... -count=1
go test ./... -count=1 -race # Requires a C compiler.

python scripts/verify-continuity.py --mode opencode --fixture kanban --compact --restart-bridge --bridge-exe <tested.exe> --base-url http://127.0.0.1:8003/v1 --work-dir <fresh-directory> --opencode <opencode.exe> --timeout 1200
python scripts/verify-continuity.py --mode codex --fixture kanban --compact --restart-bridge --bridge-exe <tested.exe> --base-url http://127.0.0.1:8004/v1 --work-dir <another-fresh-directory> --codex <codex.exe> --timeout 1200
```

Omit `--compact --restart-bridge` for ordinary continuation. Live tests need a
configured bridge account and `M365BRIDGE_API_KEY`; the harness defaults its bridge
working directory to `C:\m365bridge`. Reports contain metadata/assertions and source
hashes. Keep generated client diagnostics, project code, and private state local.

These results measure this controlled project, client versions and backend. They
do not establish reliability for arbitrary projects, unlimited history, or all
Microsoft outages. Responses retention/capsule limits and opt-out behavior are
documented in the [README](../README.md#retained-responses-and-plan-checkpoints).
