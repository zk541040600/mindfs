# MindFS Web Performance Baseline

Captured: 2026-07-12
Repository base: `main` at `d2a19fe`

## Production build baseline

Command:

```bash
cd web
npm run typecheck
./node_modules/.bin/vite build --sourcemap --outDir /tmp/mindfs-bundle-analyze-20260712 --emptyOutDir
```

Primary output:

- `assets/index-CrhvBMEd.js`: 2,128.58 kB minified, 634.05 kB gzip estimate.
- `assets/index-DQ-nGmES.css`: about 96 kB.
- Mermaid is already dynamically split; its core and diagram implementations are not part of the entry chunk.
- The Go static handler uses `http.ServeFile` and does not itself negotiate gzip or Brotli.
- Hashed `assets/` responses already use `Cache-Control: public, max-age=31536000, immutable`.

## Directional sourcemap evidence

Raw source bytes in the entry sourcemap are directional, not exact minified contribution:

- application source: 1,623,975 bytes, including `App.tsx` at 541,825 bytes;
- KaTeX: 601,155 bytes;
- React DOM: 545,403 bytes;
- parse5: 275,072 bytes;
- zod: 257,394 bytes;
- Lexical: 132,927 bytes;
- Prism: 123,110 bytes;
- JSON renderer packages and their UI registry contribute additional optional-feature dependencies.

## Confirmed optimization boundaries

1. Normal web builds can emit precompressed immutable JS/CSS siblings; native app-shell builds should not carry them.
2. `Renderer`, task-template/scheduled-task dialogs, file viewer, and Git diff viewer are optional at startup and initially synchronously imported by `App.tsx`. `TokenEditor` is also used by the core ActionBar and is not a valid optional split boundary.
3. `useSessionStream` increments React state for every stream event; visual refresh can be bounded to one update per animation frame while terminal state remains immediate.
4. Service worker runtime caching already caches dynamically requested same-origin assets after first use. The existing shell precache intentionally includes only `assets/index-*`, avoiding eager download of all Mermaid chunks.

## Baseline constraints

- Do not silence the Vite warning by increasing `chunkSizeWarningLimit`.
- Do not precompress or add unused compressed files to Android/Harmony app-shell packages.
- Preserve relay `/mindfs-assets/` aliases, content type, `Vary`, HEAD behavior, and identity fallback.
- Preserve request-scoped reconnect/pending behavior from the reliability task.
