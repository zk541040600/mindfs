#!/usr/bin/env node

import fs from "node:fs";
import path from "node:path";
import { brotliDecompressSync, gunzipSync } from "node:zlib";

const outDir = path.resolve(process.argv[2] || "dist");
const indexPath = path.join(outDir, "index.html");
const ENTRY_JS_MAX_BYTES = 1_830_000;
const ENTRY_GZIP_MAX_BYTES = 540_000;
const ENTRY_BROTLI_MAX_BYTES = 500_000;
const REQUIRED_OPTIONAL_CHUNKS = [
  "Renderer-",
  "FileViewer-",
  "GitDiffViewer-",
  "ScheduledAgentTaskDialog-",
  "TaskTemplateDialog-",
];

function fail(message) {
  console.error(`[build-performance] FAIL: ${message}`);
  process.exitCode = 1;
}

function referencedEntryAssets(html) {
  const assets = new Set();
  for (const match of html.matchAll(/<script\b[^>]*\bsrc=["']([^"']+)["'][^>]*>/gi)) {
    assets.add(match[1]);
  }
  for (const match of html.matchAll(/<link\b[^>]*\brel=["']stylesheet["'][^>]*\bhref=["']([^"']+)["'][^>]*>/gi)) {
    assets.add(match[1]);
  }
  return [...assets]
    .map((value) => value.split(/[?#]/, 1)[0])
    .map((value) => value.replace(/^\.\//, "").replace(/^\//, ""))
    .filter((value) => value.endsWith(".js") || value.endsWith(".css"));
}

if (!fs.existsSync(indexPath)) {
  fail(`missing ${indexPath}`);
} else {
  const html = fs.readFileSync(indexPath, "utf8");
  const assets = referencedEntryAssets(html);
  const entryJS = assets.filter((asset) => asset.endsWith(".js"));
  if (entryJS.length !== 1) {
    fail(`expected exactly one entry JS asset, found ${entryJS.length}`);
  }

  const emittedAssetNames = fs.readdirSync(path.join(outDir, "assets"));
  for (const prefix of REQUIRED_OPTIONAL_CHUNKS) {
    if (!emittedAssetNames.some((name) => name.startsWith(prefix) && name.endsWith(".js"))) {
      fail(`missing optional dynamic chunk ${prefix}*.js`);
    }
  }

  const serviceWorkerPath = path.join(outDir, "service-worker.js");
  if (!fs.existsSync(serviceWorkerPath)) {
    fail("missing service-worker.js");
  } else {
    const serviceWorker = fs.readFileSync(serviceWorkerPath, "utf8");
    if (/\.(?:br|gz)["']/.test(serviceWorker)) {
      fail("service worker precache contains encoded sidecars");
    }
    for (const prefix of REQUIRED_OPTIONAL_CHUNKS) {
      if (serviceWorker.includes(`assets/${prefix}`)) {
        fail(`service worker eagerly precaches optional chunk ${prefix}*.js`);
      }
    }
  }

  for (const asset of assets) {
    const identityPath = path.join(outDir, asset);
    if (!fs.existsSync(identityPath)) {
      fail(`missing referenced asset ${asset}`);
      continue;
    }
    const identity = fs.readFileSync(identityPath);
    const identityBytes = identity.length;
    const sizes = { identity: identityBytes };
    for (const suffix of [".gz", ".br"]) {
      const encodedPath = identityPath + suffix;
      if (!fs.existsSync(encodedPath)) {
        fail(`missing precompressed asset ${asset}${suffix}`);
        continue;
      }
      const encoded = fs.readFileSync(encodedPath);
      const encodedBytes = encoded.length;
      sizes[suffix.slice(1)] = encodedBytes;
      if (encodedBytes >= identityBytes) {
        fail(`${asset}${suffix} is not smaller than identity`);
      }
      try {
        const decoded = suffix === ".br"
          ? brotliDecompressSync(encoded)
          : gunzipSync(encoded);
        if (!decoded.equals(identity)) {
          fail(`${asset}${suffix} does not decode to the identity asset`);
        }
      } catch (error) {
        fail(`${asset}${suffix} is invalid: ${error instanceof Error ? error.message : String(error)}`);
      }
    }
    console.log(`[build-performance] ${asset} identity=${sizes.identity} gzip=${sizes.gz ?? "missing"} brotli=${sizes.br ?? "missing"}`);

    if (asset.endsWith(".js")) {
      if (identityBytes > ENTRY_JS_MAX_BYTES) {
        fail(`entry JS ${identityBytes} exceeds ${ENTRY_JS_MAX_BYTES} bytes`);
      }
      if ((sizes.gz ?? Number.POSITIVE_INFINITY) > ENTRY_GZIP_MAX_BYTES) {
        fail(`entry gzip ${sizes.gz ?? "missing"} exceeds ${ENTRY_GZIP_MAX_BYTES} bytes`);
      }
      if ((sizes.br ?? Number.POSITIVE_INFINITY) > ENTRY_BROTLI_MAX_BYTES) {
        fail(`entry brotli ${sizes.br ?? "missing"} exceeds ${ENTRY_BROTLI_MAX_BYTES} bytes`);
      }
    }
  }
}

if (!process.exitCode) {
  console.log("[build-performance] PASS");
}
