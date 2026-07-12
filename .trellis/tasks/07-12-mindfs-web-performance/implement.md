# Implementation Plan

## 1. Establish evidence and failing-first checks

- [x] Capture entry bundle, dependency, static-serving, service-worker, and stream-update baselines.
- [x] Define build, transport, lazy-loading, interaction, and compatibility acceptance criteria.
- [x] Add focused static encoding tests that fail before server support exists.
- [x] Add a build-performance checker that fails before compressed siblings exist.

## 2. Build and HTTP transport

- [x] Generate smaller Brotli/gzip siblings for normal web JS/CSS builds.
- [x] Skip precompression for Android/Harmony app-shell builds.
- [x] Negotiate available representations with correct q-value, content type, `Vary`, cache, relay, HEAD, and identity semantics.
- [x] Run focused Go and build checks.

## 3. Frontend startup and interaction

- [x] Move optional JSON renderer, task dialogs, file viewer, and Git diff viewer behind explicit lazy boundaries; keep the core ActionBar/TokenEditor synchronous.
- [x] Ensure closed optional dialogs do not mount and trigger downloads.
- [x] Coalesce non-terminal stream version notifications to one animation-frame update and clean up scheduled work.
- [x] Run typecheck and focused Playwright regressions.

## 4. Full validation and measurement

- [x] Run full Go test/vet, web typecheck/build, Android/Harmony app-shell builds, complete Playwright, bridge syntax, and diff checks.
- [x] Compare baseline and optimized entry/encoded sizes; inspect emitted chunks and service-worker precache.
- [x] Review content-negotiation edge cases, dynamic-import failure behavior, offline/runtime caching trade-offs, and render lifecycle.
- [x] Record evidence and update applicable performance conventions.

## 5. Controlled production verification

- [x] Present deployment impact and obtain explicit approval before replacing `web/dist` or restarting `mindfs.service`.
- [x] Snapshot binary, web assets, service PID, health, and rollback paths.
- [x] Deploy with rollback support and verify encoded direct/relay assets, browser loading, dynamic features, WebSocket behavior, and service health.
- [x] Roll back automatically if any required check fails; attempt 1 proved rollback before corrected attempt 2 passed.

## 6. Finish

- [x] Present the required one-shot commit plan and obtain confirmation.
- [x] Commit only approved source, tests, task evidence, and spec updates; exclude generated dist, binaries, logs, backups, and temporary analysis.
- [ ] Validate task metadata and archive the task; record the completed session afterward.
