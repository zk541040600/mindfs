# Design: MindFS Web Performance Optimization

## 1. Baseline and ownership

The performance path crosses four owners:

```text
Vite bundle
  -> compressed immutable siblings in web/dist/assets
  -> Go static representation negotiation
  -> browser HTTP cache / service-worker runtime cache
  -> React component and stream update scheduling
```

The build owns which representations exist. The HTTP handler owns content negotiation and headers. The service worker owns offline/runtime reuse. React owns when optional code is evaluated and how often stream events trigger rendering.

## 2. Build-time precompression

Add a local Vite plugin using Node `zlib` and `closeBundle`:

- only normal web production builds;
- recursively inspect emitted `assets/` files;
- compress `.js` and `.css` above a small threshold;
- write `.br` and `.gz` only when smaller than identity;
- never include compressed siblings in the Rollup bundle manifest or service-worker precache list;
- skip entirely when `VITE_APP_SHELL=1`.

Brotli and gzip are generated once at build time so request handling does no compression CPU work.

Add `web/scripts/check-build-performance.mjs` and invoke it from the normal `npm run build`. It resolves entry JS/CSS from `index.html`, verifies compressed siblings and ratios, reports bytes, and enforces minified/encoded budgets calibrated after the split.

## 3. Static representation negotiation

Before `http.ServeFile` serves a non-rewritten asset:

1. Parse `Accept-Encoding` tokens and q-values.
2. Rank supported available representations by client quality, preferring Brotli on ties.
3. Set `Content-Encoding` for a chosen sibling.
4. Set `Content-Type` from the original asset extension, not `.br`/`.gz`.
5. Merge `Vary: Accept-Encoding` without overwriting existing values.
6. Keep existing immutable cache headers and identity fallback.

Only existing sidecar files are selected, preserving compatibility with older installations. Relayed `/mindfs-assets/` paths already normalize to `assets/`, so they use the same owner. Rewritten `index.html` and `service-worker.js` remain uncompressed in this iteration.

## 4. Optional frontend boundaries

Use `React.lazy` with named-export adapters and explicit `Suspense` fallbacks for features that are not part of the default session shell:

- JSON plugin `Renderer` and its shadcn registry;
- scheduled-agent and task-template dialogs, mounted only while open;
- file and Git diff viewers where their current conditional branches already provide a stable load boundary.

`TokenEditor` remains synchronous because the core `ActionBar` imports and renders it; an attempted secondary dynamic import from the task editor does not create a chunk and only produces a Vite warning. Delaying the primary composer would trade startup availability for a cosmetic chunk boundary, so it is explicitly out of scope.

Do not lazy-load `AppShell`, `SessionViewer`, `SessionList`, or the default content shell. Dynamic chunks continue through same-origin URLs and are placed in the existing service-worker runtime cache after first use. As with the already-dynamic Mermaid renderer, a feature never used online is not guaranteed available during a later cold-offline first use; this trade-off is recorded rather than hiding it through eager precache.

## 5. Stream update batching

`useSessionStream` keeps a single scheduled animation frame for non-terminal stream version notifications:

- the first event schedules one increment;
- additional events before the frame reuse that schedule;
- recovery/error/done paths cancel pending work and update terminal/status state immediately;
- cleanup cancels the frame and prevents updates after unmount/session switch.

Request/event ordering remains owned by `SessionService`; this only bounds React notification frequency.

## 6. Verification

Focused checks:

- table-driven Go static encoding tests including q-values and relay aliases;
- build-performance checker against fresh dist;
- app-shell output scan for forbidden compressed siblings;
- Playwright existing pending/reconnect/extension UI matrix to exercise dynamic imports and stream behavior;
- entry sourcemap inspection to verify optional dependencies moved out of the entry;
- production check only after explicit approval.

## 7. Rollback

All behavior falls back safely when sidecars are absent. A deployment retains the prior binary and prior `web/dist` snapshot. Rollback restores both because serving a new binary with an old dist is supported but would lose compression, while serving an old binary with a new dist safely ignores sidecars.
