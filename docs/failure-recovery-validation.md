# Failure-recovery validation

## Release status

**The final candidate passed local and live verification.** The tested binary is
`70c0bfdf9d348429a09e8517103f7820788e0260106ab09fa47686abcf1d68ef`.
Full package/race validation (including the 19 SDK contracts), vet, Staticcheck,
modernize, gofmt, complexity ≤10, Python syntax and the fault-proxy framing test
passed locally. The Windows fresh-install/data-preserving-upgrade test also passed
in a disposable directory (`recovery-installation-01`). GitHub CI checks the
published commit with Linux race/SDK tests, cross-platform builds, Docker and a
Windows installation test.

The final candidate passed all six live API checks (`recovery-api-02`), generated
and downloaded a 1254×1254 PNG (`recovery-images-02`), and correctly inspected the
image supplied through a tool result (`recovery-inspection-01`).

Live verification was temporarily blocked on September 14, 2026: both the
unchanged installed `1ef77dc` build and the candidate returned HTTP 502 / empty
turns for simple prompts. Both `gpt-5.6-reasoning` and `auto` later answered again
without a bridge code change. The earlier availability failure's cause remains
unconfirmed. A separate image-download token renewal failure was resolved through
the configured account's actual Microsoft browser image-authorization flow;
repeating the main chat login alone did not resolve that separate token flow.

## Deterministic fault injection

The Go suite exercises the real HTTP handlers with a controlled model transport.
It covers all three inference formats, both buffered and streaming:

- Transient timeout, closed connection, empty-turn, HTTP 500/503 and rate-limit
  recovery before output. An explicit Retry-After delay is respected.
- Bounded attempts and a shared per-request extra-attempt budget; no retry for
  permanent authentication/permission/request failures.
- Errors and missing terminal frames after partial output produce an explicit
  interruption and no successful terminal event.
- Idle and cancelled attempts release their producer. Client disconnection is
  checked in all six protocol/mode combinations, including an idempotency key.
- Exact buffered/SSE replay after server restart, preserving response and tool IDs.
- Eight concurrent duplicates across two server instances sharing a store execute
  once; changed body/session and other credentials are handled separately.
- A real helper process is killed after reserving a request. Another server refuses
  to execute the indeterminate operation again.
- Retention opt-out, oversized replay bodies, cancellation tombstones and storage
  limits are checked explicitly.
- Observed short future-tense coding announcements cannot silently complete a
  tool-enabled turn after bounded recovery.

### Long-session stress

`TestLongSessionFaultInjection140ToolTurnsPerProtocol` verifies **140 unique client
tool calls per protocol** (420 total), intermittent injected 503s, **five compactions
and three server restarts per session**, retained pending plans and credential
isolation. Responses uses actual `previous_response_id` chains and encrypted
compaction capsules. Chat and Anthropic simulate client-side history compaction
and restore their acknowledged plan from the explicit session checkpoint.
Tool results change each turn to represent genuine progress.

The observed attempt counts were 148 for Chat, 154 for Responses (including
compaction calls) and 148 for Anthropic. The suite also passes under the Windows
Go race detector. The 19 existing official SDK contracts remain part of validation.

## Live coding-continuity challenge

Environment: Windows, Go 1.26.6, Python 3.12, OpenCode 1.17.11, Codex 0.154.0,
`gpt-5.6-reasoning` through Microsoft 365.

Each run creates an isolated SQLite/WSGI Kanban project, adds three requirements
and a status question during its failing baseline, performs a verified client
compaction, then queues four further coding rounds. The final acceptance suite
has **36 tests**: the original 20 plus 16 reporting-function checks. The functions
cover column counts, WIP capacity, ordered titles and optimistic-version reporting.
Acceptance files stay fixed throughout each run and are independently rerun.

The loopback-only test proxy injects HTTP 429, HTTP 503, a truncated chunked HTTP
stream, and an unexpected candidate restart during an active request. This tests
the client/bridge network boundary. The deterministic Go suite separately injects
failures at the Microsoft-transport boundary. Production has no fault-injection
header or fault-injection configuration option.

| Local run | Build prefix | Duration | Requests | Result |
|-----------|--------------|----------|----------|--------|
| `recovery-codex-07` | **`70c0bfdf` (final)** | **484.94 s** | **26** | All four faults; 36/36 + independent pass; no explicit resume |
| `recovery-opencode-07` | **`70c0bfdf` (final)** | **825.33 s** | **40** | All four faults; 36/36 + independent pass; two explicit resumes |
| `recovery-opencode-02` | `f71c34b4` | 1101.66 s | 55 | All four faults; 36/36 + independent pass |
| `recovery-codex-03` | `7cb63f50` | 328.61 s | 20 | All four faults; 36/36 + independent pass |
| `recovery-opencode-04` | `7cb63f50` | 619.45 s | 38 | All four faults; 36/36 + independent pass |

The final Codex run recovered without a harness-issued resume. The final OpenCode
run recorded two explicit interrupted-reply errors: one resume followed an injected
interruption, and one followed a real upstream interruption. Its acknowledged work
survived both, and it finished all four additional coding rounds. These are recovery
results with explicit resumes, not a claim of uninterrupted automatic execution.

The intermediate successful runs in the table needed no harness-issued resume.
The harness supports at most two explicit resumes for genuine upstream interruption
errors, separately from the controlled fault events, and reports both counts. It
does not treat unfinished work as a pass.

### Issues found and retained in the record

- The original fault proxy used close-delimited HTTP. Its forced EOF could look
  like a normal end to a client. It now uses chunked framing, and a dedicated test
  verifies that an injected disconnect raises `IncompleteRead`.
- Codex twice stopped after announcing future implementation/correction. Shared
  guards now recognize the observed coding-announcement forms and enforce bounded
  recovery or an explicit `task_incomplete` error.
- The WIP fixture compared a card against its state before a different successful
  move, falsely rejecting legitimate source-column renumbering. The generator now
  snapshots the card immediately before the rejected operation. Each subsequent
  run still uses immutable checks.
- Intermediate runs failed with empty turns before meaningful work, including on
  the unchanged installed bridge. Publication was held until availability returned
  and final-build native, API and image checks actually passed.
- Initial OpenCode failures now fail the harness promptly instead of waiting for
  the full coding deadline when no baseline command was ever started.

## Commands

```powershell
$env:M365_SDK_PYTHON = '<SDK venv>\Scripts\python.exe'
go test ./... -count=1 -race
python tests/fault_proxy_test.py

python scripts/verify-continuity.py --mode opencode --fixture kanban --compact --faults --recovery-rounds 4 --bridge-exe <candidate.exe> --base-url http://127.0.0.1:8003/v1 --work-dir <fresh-directory> --opencode <opencode.exe> --timeout 1800
python scripts/verify-continuity.py --mode codex --fixture kanban --compact --faults --recovery-rounds 4 --bridge-exe <candidate.exe> --base-url http://127.0.0.1:8004/v1 --work-dir <another-fresh-directory> --codex <codex.exe> --timeout 1800
```

Live commands require the configured Microsoft account and a private
`M365BRIDGE_API_KEY`. Generated projects, diagnostics, private state and reports
remain outside the repository. `recorded_check_runs` includes earlier independent
harness checks as well as client-issued checks; it is not a count of model turns.
