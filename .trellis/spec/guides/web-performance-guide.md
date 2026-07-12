# Web Performance Guide

> **Purpose**: Preserve measurable startup and interaction performance across the Vite build, Go static server, service worker, native shells, and streaming React UI.

## Measure Before Changing

For changes that can affect startup, record at least:

- entry JavaScript identity, gzip, and Brotli bytes from a clean production build;
- emitted dynamic chunks for optional features;
- whether the browser receives a compressed representation;
- cold-load JavaScript parse/evaluate work on a representative mobile browser when runtime behavior is in scope.

Do not treat Vite's chunk warning as the metric. Raising `chunkSizeWarningLimit` is not an optimization.

## Static Representation Contract

Normal web production builds generate `.br` and `.gz` siblings for sufficiently large hashed JavaScript and CSS assets. Android and Harmony app-shell builds must not include those server-only siblings.

The Go static handler owns representation negotiation:

- sidecars are optional, so older installations fall back to identity;
- parse `Accept-Encoding` quality values and honor explicit `q=0` disables;
- prefer Brotli only when client quality is tied or higher;
- set `Content-Encoding` for the selected sibling;
- derive `Content-Type` from the identity asset extension;
- merge `Vary: Accept-Encoding` without dropping existing values;
- preserve immutable cache headers for hashed `assets/` files;
- apply the same logic after `/mindfs-assets/` relay aliases normalize to `assets/`.

Compression happens at build time. Do not add per-request compression CPU for immutable bundles without new evidence that dynamic compression is required.

## Live Static-Directory Safety

The installed service may read `web/dist` directly on every request. In that layout, running the default `npm run build` changes live frontend assets even without a binary restart.

- Use a temporary `--outDir` for analysis and pre-deployment validation.
- Treat any build targeting the configured live static directory as a production change.
- A controlled deployment builds or copies into a versioned staging directory, snapshots the prior dist, stops the service, swaps the directory, and starts the service.
- Rollback restores the binary and the matching pre-task dist together.

## Code-Splitting Contract

Use dynamic imports only for components that are already behind a real conditional rendering boundary. A dynamic import of a module that another core component imports synchronously does not create a useful split.

Keep the core shell, session viewer, session list, and primary composer synchronous unless a measured design explicitly accepts delayed availability. Optional plugin renderers, file/diff viewers, and closed dialogs may use `React.lazy` with an explicit `Suspense` fallback.

`manualChunks` alone changes file layout but does not reduce initial download when the entry still imports every chunk synchronously.

## Service-Worker Boundary

The shell precache contains entry `assets/index-*` files, not encoded sidecars and not every optional or Mermaid chunk. Same-origin dynamic chunks enter the runtime cache after first online use.

When adding a new core lazy chunk, decide explicitly whether cold-offline startup requires precaching it. Do not silently expand the shell precache to every emitted JavaScript file; Mermaid's diagram implementations make that prohibitively large.

## Streaming UI Updates

Request/event ordering remains owned by `SessionService`. React notification frequency may be coalesced, but terminal semantics may not be delayed:

- batch non-terminal stream version updates to at most one per animation frame;
- recovery, message completion, and error state publish promptly;
- cancel scheduled frames on terminal callbacks, session switches, and unmount;
- preserve request-ID and pending-state guards.

## Required Verification

- focused static negotiation tests, including Brotli/gzip quality, disabled encodings, identity fallback, relay aliases, content type, `Vary`, cache, and HEAD;
- clean normal build plus the build-performance budget checker;
- Android and Harmony app-shell builds with zero `.br`/`.gz` outputs;
- TypeScript typecheck and complete Playwright suite;
- bundle comparison against the task's frozen baseline;
- production response/header and browser smoke only after an explicit deployment approval.
