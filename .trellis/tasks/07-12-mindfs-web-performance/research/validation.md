# MindFS Web Performance Validation

Validated: 2026-07-12
Implementation base at deployment: `049b637` plus the task working tree
Final performance code commit: `19f2268`

## Bundle and representation results

| Metric | Baseline | Optimized | Change |
| --- | ---: | ---: | ---: |
| Entry JS identity | 2,152,577 bytes | 1,789,008 bytes | -16.89% |
| Entry JS gzip | about 634,050 bytes | 524,450 bytes | -17.29% |
| Entry JS Brotli | not emitted | 464,004 bytes | new representation |
| Entry CSS identity | 94,715 bytes | 94,715 bytes | unchanged |
| Entry CSS gzip | not emitted | 20,233 bytes | new representation |
| Entry CSS Brotli | not emitted | 18,236 bytes | new representation |

The clean normal build emitted 96 encoded sidecars. The checker decoded entry `.br` and `.gz` back to byte-identical identity assets, enforced entry budgets, found all required optional chunks, and confirmed that the service-worker precache includes neither encoded sidecars nor optional chunks.

Measured optional chunks include:

- `Renderer`: 275.22 kB identity, 75,666 bytes Brotli in production;
- `FileViewer`: 38.75 kB identity;
- `GitDiffViewer`: 10.76 kB identity;
- `ScheduledAgentTaskDialog`: 15.65 kB identity;
- `TaskTemplateDialog`: 20.48 kB identity.

`TokenEditor` remains in the entry because the primary ActionBar imports it synchronously. An attempted secondary lazy import correctly produced a Vite warning and was removed rather than preserving a cosmetic split.

The existing advisory warning remains for the 1.77 MB core entry, Mermaid core, and large optional diagram chunks. `chunkSizeWarningLimit` was not raised.

## Build and test matrix

Passed:

- focused static representation tests for Brotli, gzip, quality preference, explicit disable, case-insensitive/wildcard parsing, invalid quality, relay alias, original content type, merged `Vary`, immutable cache, HEAD, and identity fallback;
- focused static tests under the race detector for 5 repetitions;
- `go test ./... -count=1`;
- `go vet ./...`;
- web TypeScript typecheck;
- normal Vite production build plus `scripts/check-build-performance.mjs`;
- Android app-shell build in a temporary output directory, with 0 `.br`/`.gz` files;
- Harmony app-shell build in a temporary output directory, with 0 `.br`/`.gz` files;
- complete Playwright suite: 21/21 passed;
- Pi bridge `node --check`;
- `git diff --check`.

## Stream scheduling review

`useSessionStream` now owns one animation-frame schedule for non-terminal version notifications. Bursts reuse the pending frame. Recovery, `message_done`, and stream errors publish immediately; terminal callbacks flush only scheduled work; cleanup cancels pending frames. Request identity and pending semantics remain in `SessionService` and were exercised by the full pending/reconnect Playwright matrix.

## Deployment evidence

### Attempt 1: deterministic rollback

Deployment `20260712-175018-performance` installed candidate binary SHA-256 `a814be6b030578230223a60144ddeddb6142b9b00059c181a6c3caaf653a65fa` and optimized Web manifest SHA-256 `7caf8c358b9508c701aa020fdfe64a28aa0d6985cd3546dbd8d0df8b89b03496`.

All HTTP representation checks passed, including decoded Brotli/gzip equality, relay alias, dynamic Renderer chunk, content headers, and HEAD. The first browser smoke then failed only because it required an idle watchdog ping within 18 seconds while normal WebSocket activity was still resetting the inactivity timer. Page status was 200, one application socket existed, and there were no connection or page errors.

The script automatically restored the prior binary and pre-task Web dist. `rollback_status=pass`; the service returned healthy, and all 58 JSONL hashes matched the pre-deployment snapshot. The smoke was corrected to open a protocol-valid probe with `client_id`, request ID, type `ping`, and empty payload, then deterministically require `pong`.

### Attempt 2: passed

Deployment `20260712-175912-performance-r2` passed with no rollback:

- old PID: `1589785`;
- new PID: `1618538`;
- running binary SHA-256: `a814be6b030578230223a60144ddeddb6142b9b00059c181a6c3caaf653a65fa`;
- running Web manifest SHA-256: `7caf8c358b9508c701aa020fdfe64a28aa0d6985cd3546dbd8d0df8b89b03496`;
- version: `v0.4.0-63-g049b637-dirty-performance-20260712-175018-performance`;
- binary rollback copy: `/root/mindfs/.mindfs/deploy-backups/mindfs-20260712-175912-performance-r2-pre-performance`;
- pre-task Web rollback copy: `/root/mindfs/.mindfs/deploy-backups/web-dist-20260712-175018-performance-pre-performance`.

Independent post-script checks passed again:

- entry identity 1,789,008 bytes, Brotli 464,004 bytes, gzip 524,450 bytes;
- decoded Brotli/gzip and relay alias match identity;
- dynamic Renderer Brotli response is valid;
- browser HTTP 200, application WebSocket present, active application ping/pong passed, no visible connection errors, and no page errors;
- relay reconnected with HTTP 101;
- Session API HTTP 200, `pending:false`, 8 exchanges, replying HTTP 200, 0 replying sessions;
- SQLite `PRAGMA quick_check=ok`, 32 sessions, 34 bindings;
- all 58 session JSONL hashes unchanged across rollback and final deployment.

## Operational finding

The running service reads `/root/mindfs/web/dist` directly. A default Vite build therefore changes live static assets without restarting the binary. This was detected after the required local build, verified immediately with a healthy browser smoke, and added to the project performance guide. Future analysis must build to a temporary output directory; controlled deployment must stage and swap the Web dist with its matching binary.

## Concurrent repository change

During this task, HEAD advanced independently from `d2a19fe` to user-authored commit `049b637 perf: reduce Trellis subagent latency`. That commit contains only four `.pi` Trellis runtime files and no performance task source. It was preserved as the new base and was not amended or reverted.

## Residual trade-offs

- Optional dynamic chunks are runtime-cached after first online use and are not guaranteed available for their first use during a later cold-offline session, matching the existing Mermaid policy.
- The primary composer remains synchronous because delaying ActionBar/TokenEditor would harm core interaction availability.
- Large core/diagram chunk warnings remain visible for future measured work.
- Fresh journal still reports pre-existing Git probes against non-repository paths; they are unrelated to asset transport or UI performance.
