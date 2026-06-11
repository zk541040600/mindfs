#!/usr/bin/env node
/**
 * Pi SDK bridge and runtime for MindFS.
 *
 * This bridge is production-wired for the default Pi SDK interactive runtime
 * and for bounded SDK-backed metadata, status, refresh, deterministic bridge
 * probes, and explicit safe transcript import. The Go `pi-rpc` path remains
 * available as an explicit rollback protocol.
 */
import { execFileSync } from "node:child_process";
import { existsSync } from "node:fs";
import { createRequire } from "node:module";
import { createInterface } from "node:readline";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";

let AuthStorage;
let createAgentSession;
let createAgentSessionFromServices;
let createAgentSessionRuntime;
let createAgentSessionServices;
let DefaultResourceLoader;
let ModelRegistry;
let SessionManager;
let SettingsManager;
let VERSION;

const BRIDGE_PROTOCOL_VERSION = 1;
const DEFAULT_CWD = "/root/mindfs";
const DEFAULT_AGENT_DIR = "/root/.pi/agent";
const MAX_RESOURCE_ITEMS = 50;
const MAX_SESSION_ITEMS = 50;
const DEFAULT_IMPORT_MAX_MESSAGES = 200;
const DEFAULT_IMPORT_MAX_BYTES = 256 * 1024;

for (const method of ["log", "info", "warn", "error", "debug"]) {
  console[method] = (...args) => process.stderr.write(`${args.map(String).join(" ")}\n`);
}

async function ensurePiSDK() {
  if (SessionManager) {
    return;
  }
  const sdk = await loadPiSDK();
  ({
    AuthStorage,
    createAgentSession,
    createAgentSessionFromServices,
    createAgentSessionRuntime,
    createAgentSessionServices,
    DefaultResourceLoader,
    ModelRegistry,
    SessionManager,
    SettingsManager,
    VERSION,
  } = sdk);
}

async function loadPiSDK() {
  const packagePath = "@earendil-works/pi-coding-agent/dist/index.js";
  const candidates = [];
  addCandidate(candidates, process.env.MINDFS_PI_SDK_MODULE);
  addCandidate(candidates, process.env.PI_SDK_MODULE_PATH);

  const require = createRequire(import.meta.url);
  try {
    addCandidate(candidates, require.resolve(packagePath));
  } catch {
    // MindFS does not have to vendor Pi SDK; the global Pi install is a valid deployment source.
  }

  try {
    const globalRoot = execFileSync("npm", ["root", "-g"], {
      encoding: "utf8",
      stdio: ["ignore", "pipe", "ignore"],
      timeout: 2000,
    }).trim();
    if (globalRoot) {
      addCandidate(candidates, join(globalRoot, packagePath));
    }
  } catch {
    // npm may be unavailable in minimal environments; report all attempted candidates below.
  }

  const errors = [];
  for (const candidate of candidates) {
    try {
      return await import(toImportSpecifier(candidate));
    } catch (error) {
      errors.push(`${candidate}: ${error?.message || String(error)}`);
    }
  }

  throw new ProbeError(
    "E_SDK_LOAD",
    "unable to resolve Pi SDK module; set MINDFS_PI_SDK_MODULE to @earendil-works/pi-coding-agent/dist/index.js or to an absolute dist/index.js path",
    { candidates, errors },
  );
}

function addCandidate(candidates, value) {
  const candidate = String(value || "").trim();
  if (!candidate || candidates.includes(candidate)) {
    return;
  }
  candidates.push(candidate);
}

function toImportSpecifier(candidate) {
  if (candidate.startsWith("file://") || candidate.startsWith("node:")) {
    return candidate;
  }
  if (candidate.startsWith(".") || candidate.startsWith("/") || existsSync(candidate)) {
    return pathToFileURL(resolve(candidate)).href;
  }
  return candidate;
}

// Keep the CLI predictable for a Go subprocess caller: every failure is a JSON object.
async function main() {
  const [command, ...argv] = process.argv.slice(2);
  if (!command || command === "help" || command === "--help" || command === "-h") {
    printJson(successResponse("help", buildHelp()));
    return;
  }

  try {
    await ensurePiSDK();

    if (command === "jsonl") {
      await runJsonl(argv);
      return;
    }

    const options = parseArgs(argv);
    let data;
    switch (command) {
      case "capabilities":
        data = await capabilitiesProbe(options);
        break;
      case "list-sessions":
        data = await listSessionsProbe(options);
        break;
      case "import-session":
        data = await importSessionProbe(options);
        break;
      case "session-smoke":
        data = await sessionSmokeProbe(options);
        break;
      case "extension-ui-smoke":
        data = await extensionUISmokeProbe(options);
        break;
      case "runtime-replacement-smoke":
        data = await runtimeReplacementSmokeProbe(options);
        break;
      default:
        throw new ProbeError("E_PARAM", `unknown command: ${command}`);
    }
    printJson(successResponse(command, data));
  } catch (error) {
    printJson(errorResponse(command, error));
    process.exitCode = 1;
  }
}

class ProbeError extends Error {
  constructor(code, message, details = undefined) {
    super(message);
    this.name = "ProbeError";
    this.code = code;
    this.details = details;
  }
}

function parseArgs(argv) {
  const options = {
    cwd: DEFAULT_CWD,
    agentDir: DEFAULT_AGENT_DIR,
    json: false,
  };
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (arg === "--json") {
      options.json = true;
    } else if (arg === "--cwd") {
      options.cwd = readValue(argv, ++i, arg);
    } else if (arg === "--agent-dir") {
      options.agentDir = readValue(argv, ++i, arg);
    } else if (arg === "--session-dir") {
      options.sessionDir = readValue(argv, ++i, arg);
    } else if (arg === "--limit") {
      options.limit = Number(readValue(argv, ++i, arg));
      if (!Number.isInteger(options.limit) || options.limit <= 0) {
        throw new ProbeError("E_PARAM", "--limit must be a positive integer");
      }
    } else if (arg === "--session-id") {
      options.sessionId = readValue(argv, ++i, arg);
    } else if (arg === "--max-messages") {
      options.maxMessages = Number(readValue(argv, ++i, arg));
      if (!Number.isInteger(options.maxMessages) || options.maxMessages <= 0) {
        throw new ProbeError("E_PARAM", "--max-messages must be a positive integer");
      }
    } else if (arg === "--max-bytes") {
      options.maxBytes = Number(readValue(argv, ++i, arg));
      if (!Number.isInteger(options.maxBytes) || options.maxBytes <= 0) {
        throw new ProbeError("E_PARAM", "--max-bytes must be a positive integer");
      }
    } else {
      throw new ProbeError("E_PARAM", `unknown argument: ${arg}`);
    }
  }
  options.cwd = resolve(options.cwd);
  options.agentDir = resolve(options.agentDir);
  if (options.sessionDir) {
    options.sessionDir = resolve(options.sessionDir);
  }
  return options;
}

function readValue(argv, index, flag) {
  const value = argv[index];
  if (!value || value.startsWith("--")) {
    throw new ProbeError("E_PARAM", `${flag} requires a value`);
  }
  return value;
}

function printJson(value) {
  process.stdout.write(`${JSON.stringify(value, null, 2)}\n`);
}

function writeJsonl(value) {
  process.stdout.write(`${JSON.stringify(value)}\n`);
}

function successResponse(command, data, id = undefined) {
  return {
    id,
    type: "response",
    command,
    success: true,
    data,
  };
}

function errorResponse(command, error, id = undefined) {
  return {
    id,
    type: "response",
    command,
    success: false,
    error: normalizeError(error),
  };
}

function normalizeError(error) {
  const code = error?.code || error?.name || "E_FAIL";
  const message = preview(error instanceof Error ? error.message : String(error), 500);
  const result = { code, message };
  if (error?.details !== undefined) {
    result.details = error.details;
  }
  return result;
}

function buildHelp() {
  return {
    protocolVersion: BRIDGE_PROTOCOL_VERSION,
    commands: [
      "capabilities --cwd /root/mindfs --agent-dir /root/.pi/agent --json",
      "list-sessions --cwd /root/mindfs --agent-dir /root/.pi/agent --json",
      "import-session --cwd /root/mindfs --session-id <id> --json",
      "session-smoke --cwd /root/mindfs --json",
      "extension-ui-smoke --json",
      "runtime-replacement-smoke --cwd /root/mindfs --json",
      "jsonl",
    ],
    notes: [
      "Pi SDK is the default interactive runtime; pi-rpc remains available as an explicit rollback protocol.",
      "Capability output includes metadata, counts, and paths only; no credential values or context file contents.",
    ],
  };
}

async function capabilitiesProbe(options) {
  const settingsManager = SettingsManager.inMemory({});
  settingsManager.setProjectTrusted(false);
  const authStorage = AuthStorage.create(join(options.agentDir, "auth.json"));
  const modelRegistry = ModelRegistry.create(authStorage, join(options.agentDir, "models.json"));
  const loader = new DefaultResourceLoader({
    cwd: options.cwd,
    agentDir: options.agentDir,
    settingsManager,
  });

  await loader.reload({
    resolveProjectTrust: async () => false,
  });

  const extensionsResult = loader.getExtensions();
  const { skills, diagnostics: skillDiagnostics } = loader.getSkills();
  const { prompts, diagnostics: promptDiagnostics } = loader.getPrompts();
  const { themes, diagnostics: themeDiagnostics } = loader.getThemes();
  const { agentsFiles } = loader.getAgentsFiles();
  const availableModels = modelRegistry.getAvailable();
  const allModels = modelRegistry.getAll();
  const modelRegistryError = modelRegistry.getError();

  return {
    protocolVersion: BRIDGE_PROTOCOL_VERSION,
    sdkAvailable: true,
    sdkVersion: VERSION,
    cwd: options.cwd,
    agentDir: options.agentDir,
    productionDefaultUnchanged: true,
    supports: buildSupports(),
    resources: {
      skills: limited(skills.map(sanitizeSkill), options.limit ?? MAX_RESOURCE_ITEMS),
      skillsCount: skills.length,
      prompts: limited(prompts.map(sanitizePrompt), options.limit ?? MAX_RESOURCE_ITEMS),
      promptsCount: prompts.length,
      extensions: limited(extensionsResult.extensions.map(sanitizeExtension), options.limit ?? MAX_RESOURCE_ITEMS),
      extensionsCount: extensionsResult.extensions.length,
      extensionErrors: extensionsResult.errors.map((entry) => ({ path: entry.path, error: normalizeError(entry.error) })),
      themes: limited(themes.map(sanitizeTheme), options.limit ?? MAX_RESOURCE_ITEMS),
      themesCount: themes.length,
      contextFiles: limited(agentsFiles.map((entry) => ({ path: entry.path })), options.limit ?? MAX_RESOURCE_ITEMS),
      contextFilesCount: agentsFiles.length,
    },
    commands: limited(collectCommands(extensionsResult, prompts, skills), options.limit ?? MAX_RESOURCE_ITEMS),
    commandCount: collectCommands(extensionsResult, prompts, skills).length,
    models: {
      availableCount: availableModels.length,
      totalCount: allModels.length,
      available: limited(availableModels.map(sanitizeModel), options.limit ?? MAX_RESOURCE_ITEMS),
    },
    diagnostics: {
      skills: sanitizeDiagnostics(skillDiagnostics),
      prompts: sanitizeDiagnostics(promptDiagnostics),
      themes: sanitizeDiagnostics(themeDiagnostics),
      modelRegistryError: modelRegistryError ? normalizeError(modelRegistryError) : undefined,
      authErrors: authStorage.drainErrors().map((error) => normalizeError(error)),
      settingsErrors: settingsManager.drainErrors().map((entry) => ({ scope: entry.scope, error: normalizeError(entry.error) })),
    },
    security: {
      projectTrustForcedFalse: true,
      rawContextContentIncluded: false,
      credentialValuesIncluded: false,
      extensionCommandsExecuted: false,
    },
  };
}

function buildSupports() {
  return {
    prompt: true,
    steer: true,
    followUp: true,
    extensionUI: true,
    sessions: true,
    fork: true,
    clone: true,
    importJsonl: true,
    compact: true,
    resources: true,
    deterministicHarness: true,
  };
}

function sanitizeSkill(skill) {
  return {
    name: skill.name,
    description: skill.description,
    path: skill.filePath,
    baseDir: skill.baseDir,
    sourceInfo: sanitizeSourceInfo(skill.sourceInfo),
    disableModelInvocation: skill.disableModelInvocation,
  };
}

function sanitizePrompt(prompt) {
  return {
    name: prompt.name,
    description: prompt.description,
    argumentHint: prompt.argumentHint,
    path: prompt.filePath,
    sourceInfo: sanitizeSourceInfo(prompt.sourceInfo),
  };
}

function sanitizeExtension(extension) {
  return {
    path: extension.path,
    resolvedPath: extension.resolvedPath,
    sourceInfo: sanitizeSourceInfo(extension.sourceInfo),
    commandsCount: extension.commands.size,
    toolsCount: extension.tools.size,
    handlersCount: Array.from(extension.handlers.values()).reduce((sum, handlers) => sum + handlers.length, 0),
    flagsCount: extension.flags.size,
    shortcutsCount: extension.shortcuts.size,
  };
}

function sanitizeTheme(theme) {
  return {
    name: theme.name,
    path: theme.sourcePath,
    sourceInfo: sanitizeSourceInfo(theme.sourceInfo),
  };
}

function sanitizeSourceInfo(sourceInfo) {
  if (!sourceInfo) {
    return undefined;
  }
  return {
    path: sourceInfo.path,
    source: sourceInfo.source,
    scope: sourceInfo.scope,
    origin: sourceInfo.origin,
    baseDir: sourceInfo.baseDir,
  };
}

function sanitizeDiagnostics(diagnostics) {
  return diagnostics.map((diagnostic) => ({
    type: diagnostic.type,
    message: preview(diagnostic.message, 500),
    path: diagnostic.path,
    sourceInfo: sanitizeSourceInfo(diagnostic.sourceInfo),
  }));
}

function sanitizeModel(model) {
  return {
    provider: model.provider,
    id: model.id,
    name: model.name,
    reasoning: model.reasoning,
    input: model.input,
    contextWindow: model.contextWindow,
    maxTokens: model.maxTokens,
  };
}

function collectCommands(extensionsResult, prompts, skills) {
  const commands = [];
  for (const extension of extensionsResult.extensions) {
    for (const command of extension.commands.values()) {
      commands.push({
        name: command.name,
        source: "extension",
        description: command.description,
        sourceInfo: sanitizeSourceInfo(command.sourceInfo),
      });
    }
  }
  for (const prompt of prompts) {
    commands.push({
      name: prompt.name,
      source: "prompt",
      description: prompt.description,
      argumentHint: prompt.argumentHint,
      sourceInfo: sanitizeSourceInfo(prompt.sourceInfo),
    });
  }
  for (const skill of skills) {
    commands.push({
      name: `skill:${skill.name}`,
      source: "skill",
      description: skill.description,
      sourceInfo: sanitizeSourceInfo(skill.sourceInfo),
    });
  }
  commands.sort((a, b) => `${a.source}:${a.name}`.localeCompare(`${b.source}:${b.name}`));
  return commands;
}

async function listSessionsProbe(options) {
  const sessionDir = options.sessionDir ?? defaultSessionDirPath(options.cwd, options.agentDir);
  const sessions = await SessionManager.list(options.cwd, sessionDir);
  const summary = await summarizeSessions(sessions, options.limit ?? MAX_SESSION_ITEMS);
  return { ...summary, sessionDir };
}

async function importSessionProbe(options) {
  const sessionId = String(options.sessionId || "").trim();
  if (!sessionId) {
    throw new ProbeError("E_PARAM", "--session-id is required");
  }
  const sessionDir = options.sessionDir ?? defaultSessionDirPath(options.cwd, options.agentDir);
  const sessions = await SessionManager.list(options.cwd, sessionDir);
  const info = sessions.find((entry) => entry.id === sessionId || entry.path === sessionId);
  if (!info) {
    throw new ProbeError("E_NOT_FOUND", `session not found: ${sessionId}`);
  }
  const manager = SessionManager.open(info.path);
  const entries = manager.getBranch?.() ?? manager.getEntries();
  return buildSafeTranscriptImport({
    sessionId: info.id,
    title: safeTitle(info.name || ""),
    entries,
    maxMessages: options.maxMessages ?? DEFAULT_IMPORT_MAX_MESSAGES,
    maxBytes: options.maxBytes ?? DEFAULT_IMPORT_MAX_BYTES,
  });
}

function buildSafeTranscriptImport({ sessionId, title, entries, maxMessages, maxBytes }) {
  const exchanges = [];
  const warnings = new Set();
  let messageCount = 0;
  let skippedCount = 0;
  let redactedCount = 0;
  let totalBytes = 0;
  let truncated = false;

  for (const entry of entries || []) {
    if (exchanges.length >= maxMessages) {
      truncated = true;
      warnings.add("max_messages_reached");
      break;
    }
    if (entry?.type !== "message") {
      if (entry?.type) {
        warnings.add(`${entry.type}_skipped`);
      }
      skippedCount++;
      continue;
    }
    messageCount++;
    const sourceRole = entry.message?.role;
    const role = sourceRole === "user" ? "user" : sourceRole === "assistant" ? "agent" : "";
    if (!role) {
      skippedCount++;
      warnings.add("unsupported_role_skipped");
      continue;
    }
    const extracted = extractSafeMessageText(entry.message, warnings);
    if (!extracted.text) {
      skippedCount++;
      warnings.add("empty_text_skipped");
      continue;
    }
    const sanitized = sanitizeTranscriptText(extracted.text);
    if (sanitized.binaryLike) {
      skippedCount++;
      warnings.add("binary_content_skipped");
      continue;
    }
    if (!sanitized.text) {
      skippedCount++;
      warnings.add("empty_text_skipped");
      continue;
    }
    redactedCount += sanitized.redactions;
    const bytes = Buffer.byteLength(sanitized.text, "utf8");
    if (totalBytes + bytes > maxBytes) {
      truncated = true;
      warnings.add("max_bytes_reached");
      break;
    }
    totalBytes += bytes;
    exchanges.push({
      role,
      content: sanitized.text,
      timestamp: normalizeTimestamp(entry.timestamp),
    });
  }

  if (!exchanges.length) {
    throw new ProbeError("E_NO_SAFE_CONTENT", "session contains no safe transcript text");
  }

  return {
    sessionId,
    title,
    messageCount,
    importedCount: exchanges.length,
    skippedCount,
    redactedCount,
    truncated,
    totalBytes,
    exchanges,
    warnings: [...warnings].sort(),
  };
}

function extractSafeMessageText(message, warnings) {
  const content = message?.content;
  if (typeof content === "string") {
    return { text: content };
  }
  if (!Array.isArray(content)) {
    return { text: "" };
  }
  const textParts = [];
  for (const part of content) {
    if (part?.type === "text" && typeof part.text === "string") {
      textParts.push(part.text);
    } else {
      warnings.add("non_text_part_skipped");
    }
  }
  return { text: textParts.join("\n") };
}

function sanitizeTranscriptText(text) {
  let value = String(text || "").replace(/\u0000/g, "").replace(/\r\n/g, "\n").trim();
  if (!value) {
    return { text: "", redactions: 0, binaryLike: false };
  }
  const controlCount = [...value].filter((ch) => {
    const code = ch.charCodeAt(0);
    return code < 32 && ch !== "\n" && ch !== "\t";
  }).length;
  if (controlCount > Math.max(8, value.length * 0.05)) {
    return { text: "", redactions: 0, binaryLike: true };
  }

  let redactions = 0;
  const replace = (pattern, replacement) => {
    value = value.replace(pattern, (...args) => {
      redactions++;
      return typeof replacement === "function" ? replacement(...args) : replacement;
    });
  };

  replace(/-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----/g, "[REDACTED:private-key]");
  replace(/\bAuthorization\s*:\s*Bearer\s+[A-Za-z0-9._~+\/-]+=*/gi, "Authorization: Bearer [REDACTED:token]");
  replace(/\b(api[_-]?key|token|secret|password|passwd|pwd)\b\s*[:=]\s*["']?[^"'\s]{8,}["']?/gi, (match, key) => `${key}=[REDACTED:secret]`);
  replace(/\b[A-Za-z0-9_\-]{32,}\.[A-Za-z0-9_\-]{16,}\.[A-Za-z0-9_\-]{16,}\b/g, "[REDACTED:jwt]");
  replace(/\b[A-Za-z0-9+\/_-]{48,}={0,2}\b/g, "[REDACTED:token]");

  return { text: value.trim(), redactions, binaryLike: false };
}

function normalizeTimestamp(timestamp) {
  if (timestamp instanceof Date) {
    return timestamp.toISOString();
  }
  if (typeof timestamp === "number" && Number.isFinite(timestamp)) {
    return new Date(timestamp).toISOString();
  }
  if (typeof timestamp === "string" && timestamp.trim()) {
    const parsed = new Date(timestamp);
    return Number.isNaN(parsed.getTime()) ? undefined : parsed.toISOString();
  }
  return undefined;
}

async function summarizeSessions(sessions, limit) {
  const limitedSessions = limited(sessions, limit);
  return {
    count: sessions.length,
    returned: limitedSessions.length,
    sessions: limitedSessions.map(sanitizeSessionInfo),
  };
}

function sanitizeSessionInfo(info) {
  const summary = {
    path: info.path,
    id: info.id,
    cwd: info.cwd,
    name: safeTitle(info.name),
    parentSessionPath: info.parentSessionPath,
    created: info.created instanceof Date ? info.created.toISOString() : info.created,
    modified: info.modified instanceof Date ? info.modified.toISOString() : info.modified,
    messageCount: info.messageCount,
    hasFirstMessage: Boolean(info.firstMessage),
  };

  try {
    const manager = SessionManager.open(info.path);
    const entries = manager.getEntries();
    const tree = manager.getTree();
    summary.entryCount = entries.length;
    summary.leafId = manager.getLeafId();
    summary.currentPathEntryCount = manager.getBranch().length;
    summary.treeRootCount = tree.length;
    summary.treeMaxDepth = maxTreeDepth(tree);
    summary.entryTypeCounts = countEntryTypes(entries);
  } catch (error) {
    summary.openError = normalizeError(error);
  }

  return summary;
}

async function sessionSmokeProbe(options) {
  const scratch = await mkdtemp(join(tmpdir(), "mindfs-pi-sdk-session-smoke-"));
  try {
    const sessionDir = options.sessionDir ?? join(scratch, "sessions");
    const manager = SessionManager.create(options.cwd, sessionDir);
    const userEntryId = manager.appendMessage({
      role: "user",
      content: [{ type: "text", text: "MindFS SDK session smoke user message" }],
      timestamp: Date.now(),
    });
    const assistantEntryId = manager.appendMessage({
      role: "assistant",
      content: [{ type: "text", text: "MindFS SDK session smoke assistant response" }],
      timestamp: Date.now(),
    });
    manager.appendSessionInfo("MindFS SDK bridge smoke session");

    const sessions = await SessionManager.list(options.cwd, sessionDir);
    const opened = SessionManager.open(manager.getSessionFile());
    const entries = opened.getEntries();
    const tree = opened.getTree();

    return {
      cwd: options.cwd,
      sessionDir,
      createdSessionFile: manager.getSessionFile(),
      sessionId: manager.getSessionId(),
      userEntryId,
      assistantEntryId,
      listed: await summarizeSessions(sessions, options.limit ?? MAX_SESSION_ITEMS),
      openedSummary: {
        header: opened.getHeader(),
        entryCount: entries.length,
        leafId: opened.getLeafId(),
        branchEntryCount: opened.getBranch().length,
        treeRootCount: tree.length,
        treeMaxDepth: maxTreeDepth(tree),
        contextMessageCount: opened.buildSessionContext().messages.length,
        entryTypeCounts: countEntryTypes(entries),
      },
      cleanup: options.sessionDir ? "preserved custom --session-dir" : "removed temporary session directory",
    };
  } finally {
    if (!options.sessionDir) {
      await rm(scratch, { recursive: true, force: true });
    }
  }
}

async function extensionUISmokeProbe(options) {
  const events = [];
  const errors = [];
  const agentDir = await mkdtemp(join(tmpdir(), "mindfs-pi-sdk-ui-agent-"));
  const cwd = options.cwd ?? DEFAULT_CWD;
  const resourceLoader = new DefaultResourceLoader({
    cwd,
    agentDir,
    noExtensions: true,
    noSkills: true,
    noPromptTemplates: true,
    noThemes: true,
    noContextFiles: true,
    extensionFactories: [makeUIProbeExtension()],
  });

  try {
    await resourceLoader.reload();
    const sessionManager = SessionManager.inMemory(cwd);
    const authStorage = AuthStorage.inMemory();
    const modelRegistry = ModelRegistry.inMemory(authStorage);
    const settingsManager = SettingsManager.inMemory({
      compaction: { enabled: false },
      retry: { enabled: false },
    });
    const { session, extensionsResult } = await createAgentSession({
      cwd,
      agentDir,
      authStorage,
      modelRegistry,
      resourceLoader,
      sessionManager,
      settingsManager,
      noTools: "all",
      thinkingLevel: "off",
    });

    try {
      await bindProbeExtensions(session, createRecordingUI(events), errors);
      await session.prompt("/ui-demo smoke-args", { expandPromptTemplates: true });
      return {
        sdkAvailable: true,
        scenario: "extension-ui",
        command: "/ui-demo smoke-args",
        realModelProviderUsed: false,
        extensionCommands: collectCommands(extensionsResult, [], []),
        events,
        eventMethods: events.map((event) => event.method),
        responses: events.filter((event) => event.response !== undefined).map((event) => ({
          id: event.id,
          method: event.method,
          response: event.response,
        })),
        sessionEntries: session.sessionManager.getEntries().map(sanitizeEntry),
        errors: errors.map(normalizeError),
        assertions: {
          emittedNotify: events.some((event) => event.method === "notify"),
          emittedStatus: events.some((event) => event.method === "setStatus"),
          emittedWidget: events.some((event) => event.method === "setWidget"),
          emittedTitle: events.some((event) => event.method === "setTitle"),
          emittedEditorText: events.some((event) => event.method === "setEditorText"),
          emittedSelect: events.some((event) => event.method === "select"),
          emittedConfirm: events.some((event) => event.method === "confirm"),
          emittedInput: events.some((event) => event.method === "input"),
          emittedEditor: events.some((event) => event.method === "editor"),
          recordedCustomEntry: session.sessionManager.getEntries().some((entry) => entry.type === "custom" && entry.customType === "mindfs.pi_sdk_bridge.ui_smoke"),
        },
      };
    } finally {
      session.dispose();
    }
  } finally {
    await rm(agentDir, { recursive: true, force: true });
  }
}

function makeUIProbeExtension() {
  return (pi) => {
    pi.registerCommand("ui-demo", {
      description: "Exercise every MindFS-relevant Pi extension UI method without an LLM call.",
      handler: async (args, ctx) => {
        ctx.ui.notify(`ui-demo args=${args}`, "info");
        ctx.ui.setStatus("mindfs.pi_sdk_bridge", "running");
        ctx.ui.setWidget("mindfs.pi_sdk_bridge", ["SDK bridge widget", "line 2"], { placement: "aboveEditor" });
        ctx.ui.setTitle("MindFS Pi SDK Bridge Smoke");
        ctx.ui.setEditorText("prefilled by ui-demo");
        const selected = await ctx.ui.select("Choose bridge route", ["rpc-first", "sdk-bridge"], { timeout: 1000 });
        const confirmed = await ctx.ui.confirm("Confirm SDK bridge", "Continue deterministic smoke?", { timeout: 1000 });
        const input = await ctx.ui.input("Bridge input", "type here", { timeout: 1000 });
        const edited = await ctx.ui.editor("Bridge editor", "initial text");
        ctx.ui.setStatus("mindfs.pi_sdk_bridge", undefined);
        ctx.ui.setWidget("mindfs.pi_sdk_bridge", undefined, { placement: "aboveEditor" });
        pi.appendEntry("mindfs.pi_sdk_bridge.ui_smoke", {
          args,
          selected,
          confirmed,
          input,
          edited,
        });
      },
    });
  };
}

function createRecordingUI(events) {
  const nextId = makeIdGenerator("ui");
  return {
    select: async (title, choices, opts) => {
      const response = choices[1] ?? choices[0];
      events.push({
        type: "extension_ui_request",
        id: nextId("select"),
        method: "select",
        title,
        options: choices,
        opts: sanitizeDialogOptions(opts),
        response,
      });
      return response;
    },
    confirm: async (title, message, opts) => {
      const response = true;
      events.push({
        type: "extension_ui_request",
        id: nextId("confirm"),
        method: "confirm",
        title,
        message,
        opts: sanitizeDialogOptions(opts),
        response,
      });
      return response;
    },
    input: async (title, placeholder, opts) => {
      const response = "typed from MindFS SDK bridge smoke";
      events.push({
        type: "extension_ui_request",
        id: nextId("input"),
        method: "input",
        title,
        placeholder,
        opts: sanitizeDialogOptions(opts),
        response,
      });
      return response;
    },
    editor: async (title, prefill) => {
      const response = `${prefill}\nedited by smoke`;
      events.push({
        type: "extension_ui_request",
        id: nextId("editor"),
        method: "editor",
        title,
        prefill,
        response,
      });
      return response;
    },
    notify: (message, notificationType) => {
      events.push({
        type: "extension_ui_request",
        id: nextId("notify"),
        method: "notify",
        message,
        notificationType,
        fireAndForget: true,
      });
    },
    onTerminalInput: () => () => {},
    setStatus: (key, text) => {
      events.push({
        type: "extension_ui_request",
        id: nextId("setStatus"),
        method: "setStatus",
        key,
        text,
        fireAndForget: true,
      });
    },
    setWorkingMessage: (message) => {
      events.push({ type: "extension_ui_request", id: nextId("setWorkingMessage"), method: "setWorkingMessage", message, fireAndForget: true });
    },
    setWorkingVisible: (visible) => {
      events.push({ type: "extension_ui_request", id: nextId("setWorkingVisible"), method: "setWorkingVisible", visible, fireAndForget: true });
    },
    setWorkingIndicator: (indicatorOptions) => {
      events.push({ type: "extension_ui_request", id: nextId("setWorkingIndicator"), method: "setWorkingIndicator", options: indicatorOptions, fireAndForget: true });
    },
    setHiddenThinkingLabel: (label) => {
      events.push({ type: "extension_ui_request", id: nextId("setHiddenThinkingLabel"), method: "setHiddenThinkingLabel", label, fireAndForget: true });
    },
    setWidget: (key, content, widgetOptions) => {
      events.push({
        type: "extension_ui_request",
        id: nextId("setWidget"),
        method: "setWidget",
        key,
        content: typeof content === "function" ? "<component-factory>" : content,
        options: widgetOptions,
        fireAndForget: true,
      });
    },
    setFooter: () => {},
    setHeader: () => {},
    setTitle: (title) => {
      events.push({ type: "extension_ui_request", id: nextId("setTitle"), method: "setTitle", title, fireAndForget: true });
    },
    custom: async () => undefined,
    pasteToEditor: (text) => {
      events.push({ type: "extension_ui_request", id: nextId("pasteToEditor"), method: "pasteToEditor", text, fireAndForget: true });
    },
    setEditorText: (text) => {
      events.push({ type: "extension_ui_request", id: nextId("setEditorText"), method: "setEditorText", text, fireAndForget: true });
    },
    getEditorText: () => "",
    addAutocompleteProvider: () => {},
    setEditorComponent: () => {},
    getEditorComponent: () => undefined,
    theme: {},
    getAllThemes: () => [],
    getTheme: () => undefined,
    setTheme: () => ({ success: false, error: "theme switching not implemented in probe UI" }),
    getToolsExpanded: () => false,
    setToolsExpanded: () => {},
  };
}

async function runtimeReplacementSmokeProbe(options) {
  const scratch = await mkdtemp(join(tmpdir(), "mindfs-pi-sdk-runtime-"));
  const cwd = options.cwd ?? DEFAULT_CWD;
  const agentDir = join(scratch, "agent");
  const sessionDir = join(scratch, "sessions");
  const authStorage = AuthStorage.inMemory();
  const modelRegistry = ModelRegistry.inMemory(authStorage);
  const settingsManager = SettingsManager.inMemory({
    compaction: { enabled: false },
    retry: { enabled: false },
  });
  const errors = [];
  const replacements = [];
  let unsubscribe;

  try {
    const createRuntime = async ({ cwd: runtimeCwd, agentDir: runtimeAgentDir, sessionManager, sessionStartEvent }) => {
      const services = await createAgentSessionServices({
        cwd: runtimeCwd,
        agentDir: runtimeAgentDir,
        authStorage,
        settingsManager,
        modelRegistry,
        resourceLoaderOptions: {
          noExtensions: true,
          noSkills: true,
          noPromptTemplates: true,
          noThemes: true,
          noContextFiles: true,
        },
      });
      return {
        ...(await createAgentSessionFromServices({
          services,
          sessionManager,
          sessionStartEvent,
          noTools: "all",
          thinkingLevel: "off",
        })),
        services,
        diagnostics: services.diagnostics,
      };
    };

    const initialManager = SessionManager.create(cwd, sessionDir);
    const userEntryId = initialManager.appendMessage({
      role: "user",
      content: [{ type: "text", text: "Runtime replacement smoke root" }],
      timestamp: Date.now(),
    });
    const assistantEntryId = initialManager.appendMessage({
      role: "assistant",
      content: [{ type: "text", text: "Runtime replacement smoke assistant" }],
      timestamp: Date.now(),
    });

    const runtime = await createAgentSessionRuntime(createRuntime, {
      cwd,
      agentDir,
      sessionManager: initialManager,
    });

    const rebind = async (session) => {
      unsubscribe?.();
      await session.bindExtensions({ onError: (error) => errors.push(error) });
      unsubscribe = session.subscribe((event) => {
        if (event.type === "queue_update") {
          replacements.push({ type: "queue_update", steering: event.steering.length, followUp: event.followUp.length });
        }
      });
      replacements.push({
        type: "rebind",
        sessionFile: session.sessionFile,
        sessionId: session.sessionId,
        entryCount: session.sessionManager.getEntries().length,
      });
    };

    runtime.setRebindSession(rebind);
    await rebind(runtime.session);
    const initial = summarizeRuntimeSession(runtime.session);
    const forkResult = await runtime.fork(assistantEntryId, { position: "at" });
    const afterFork = summarizeRuntimeSession(runtime.session);
    const newResult = await runtime.newSession();
    const afterNew = summarizeRuntimeSession(runtime.session);

    await runtime.dispose();
    unsubscribe = undefined;

    return {
      cwd,
      agentDir,
      sessionDir,
      userEntryId,
      assistantEntryId,
      initial,
      forkResult,
      afterFork,
      newResult,
      afterNew,
      replacements,
      assertions: {
        newSessionChangedFile: Boolean(initial.sessionFile && afterNew.sessionFile && initial.sessionFile !== afterNew.sessionFile),
        forkCreatedParentLinkedSession: Boolean(afterFork.parentSession),
        rebindCalledForInitialForkAndNew: replacements.filter((entry) => entry.type === "rebind").length >= 3,
      },
      errors: errors.map(normalizeError),
      cleanup: "removed temporary agent/session directory",
    };
  } finally {
    unsubscribe?.();
    await rm(scratch, { recursive: true, force: true });
  }
}

function summarizeRuntimeSession(session) {
  const header = session.sessionManager.getHeader();
  return {
    sessionFile: session.sessionFile,
    sessionId: session.sessionId,
    headerId: header?.id,
    parentSession: header?.parentSession,
    entryCount: session.sessionManager.getEntries().length,
    leafId: session.sessionManager.getLeafId(),
    treeRootCount: session.sessionManager.getTree().length,
  };
}

async function bindProbeExtensions(session, uiContext, errors) {
  await session.bindExtensions({
    uiContext,
    mode: "rpc",
    commandContextActions: {
      waitForIdle: async () => {},
      newSession: async () => ({ cancelled: false }),
      fork: async () => ({ cancelled: false }),
      navigateTree: async () => ({ cancelled: false }),
      switchSession: async () => ({ cancelled: false }),
      reload: async () => {},
    },
    abortHandler: () => {},
    shutdownHandler: () => {},
    onError: (error) => errors.push(error),
  });
}

async function runJsonl(argv) {
  const baseOptions = parseArgs(argv);
  let runtime;
  try {
    for await (const rawLine of createInterface({ input: process.stdin, crlfDelay: Infinity })) {
      const line = rawLine.trim();
      if (!line) {
        continue;
      }
      let request;
      try {
        request = JSON.parse(line);
      } catch (error) {
        writeJsonl(errorResponse("jsonl", new ProbeError("E_JSON", "invalid JSON request", { rawLine: preview(line) })));
        continue;
      }

      try {
        if (request.type === "start_test_runtime") {
          await runtime?.dispose?.();
          runtime = createJsonlTestRuntime(request.scenario);
          writeJsonl({ id: request.id, type: "response", command: "start_test_runtime", success: true, data: { scenario: request.scenario } });
        } else if (request.type === "start_sdk_runtime") {
          await runtime?.dispose?.();
          runtime = await createJsonlSDKRuntime({ ...baseOptions, cwd: resolve(request.cwd ?? baseOptions.cwd), agentDir: resolve(request.agentDir ?? baseOptions.agentDir), model: request.model });
          writeJsonl({ id: request.id, type: "response", command: "start_sdk_runtime", success: true, data: { cwd: runtime.cwd, agentDir: runtime.agentDir } });
        } else if (request.type === "prompt") {
          if (!runtime) {
            throw new ProbeError("E_STATE", "runtime must be started before prompt");
          }
          await runtime.prompt(request);
        } else if (request.type === "extension_ui_response") {
          if (!runtime) {
            throw new ProbeError("E_STATE", "runtime must be started before extension_ui_response");
          }
          await runtime.answerExtensionUI(request);
        } else if (await dispatchJsonlRuntimeControl(runtime, request)) {
          // Handled by the active JSONL runtime.
        } else if (request.type === "capabilities") {
          const data = await capabilitiesProbe({ ...baseOptions, cwd: resolve(request.cwd ?? baseOptions.cwd), agentDir: resolve(request.agentDir ?? baseOptions.agentDir) });
          writeJsonl(successResponse("capabilities", data, request.id));
        } else if (request.type === "list_sessions") {
          const data = await listSessionsProbe({ ...baseOptions, cwd: resolve(request.cwd ?? baseOptions.cwd), agentDir: resolve(request.agentDir ?? baseOptions.agentDir) });
          writeJsonl(successResponse("list_sessions", data, request.id));
        } else {
          throw new ProbeError("E_PARAM", `unknown jsonl request type: ${request.type}`);
        }
      } catch (error) {
        writeJsonl(errorResponse(request.type ?? "jsonl", error, request.id));
      }
    }
  } finally {
    await runtime?.dispose?.();
  }
}

async function dispatchJsonlRuntimeControl(runtime, request) {
  const handlers = {
    get_state: "getState",
    get_available_models: "getAvailableModels",
    set_model: "setModel",
    set_thinking_level: "setThinkingLevel",
    get_commands: "getCommands",
    abort: "abort",
  };
  const method = handlers[request.type];
  if (!method) {
    return false;
  }
  if (!runtime) {
    throw new ProbeError("E_STATE", `runtime must be started before ${request.type}`);
  }
  if (typeof runtime[method] !== "function") {
    throw new ProbeError("E_PARAM", `${runtime.kind ?? "jsonl"} runtime does not support ${request.type}`);
  }
  await runtime[method](request);
  return true;
}

function createJsonlTestRuntime(scenario) {
  if (scenario === "extension-ui") {
    return createJsonlUIRuntime();
  }
  if (scenario === "prompt-stream") {
    return createJsonlPromptRuntime();
  }
  if (scenario === "message-end-only") {
    return createJsonlMessageEndOnlyRuntime();
  }
  if (scenario === "tool-events") {
    return createJsonlToolRuntime();
  }
  if (scenario === "turn-end-only") {
    return createJsonlTurnEndOnlyRuntime();
  }
  if (scenario === "slash-controls") {
    return createJsonlSlashRuntime();
  }
  if (scenario === "runtime-controls") {
    return createJsonlControlsRuntime();
  }
  if (scenario === "abort-hangs") {
    return createJsonlAbortHangsRuntime();
  }
  throw new ProbeError("E_PARAM", `unsupported test runtime scenario: ${scenario}`);
}

function createJsonlUIRuntime() {
  const runtime = {
    kind: "extension-ui",
    pending: new Map(),
    responses: [],
    prompt: async function (request) {
      if (request.message !== "/ui-demo") {
        throw new ProbeError("E_PARAM", "extension-ui jsonl smoke supports only /ui-demo");
      }
      for (const event of buildJsonlUIEvents()) {
        runtime.pending.set(event.id, event);
        writeJsonl(event);
      }
      writeJsonl({ id: request.id, type: "response", command: "prompt", success: true, data: { queuedUIRequests: runtime.pending.size } });
    },
    answerExtensionUI: async function (request) {
      const pending = runtime.pending.get(request.id);
      if (!pending) {
        throw new ProbeError("E_PARAM", `unknown extension UI request id: ${request.id}`);
      }
      runtime.pending.delete(request.id);
      runtime.responses.push({ id: request.id, method: pending.method, value: request.value, confirmed: request.confirmed, text: request.text });
      writeJsonl({ id: request.id, type: "response", command: "extension_ui_response", success: true, data: { method: pending.method, remaining: runtime.pending.size } });
    },
    dispose: async function () {},
  };
  return runtime;
}

function createJsonlPromptRuntime() {
  return {
    kind: "prompt-stream",
    pending: new Map(),
    responses: [],
    prompt: async function (request) {
      const message = String(request.message ?? "").trim();
      if (!message) {
        throw new ProbeError("E_PARAM", "prompt message required");
      }
      if (message.includes("fail")) {
        writeJsonl(errorResponse("prompt", new ProbeError("E_TEST_PROMPT", "deterministic prompt failure"), request.id));
        return;
      }
      writeJsonl(successResponse("prompt", { scenario: "prompt-stream" }, request.id));
      writeJsonl({ type: "agent_start" });
      writeJsonl({ type: "message_start", message: { role: "assistant", content: [] } });
      writeJsonl({
        type: "message_update",
        message: { role: "assistant", content: [{ type: "text", text: `sdk prompt: ${message}` }] },
        assistantMessageEvent: { type: "text_delta", delta: "sdk prompt: " },
      });
      writeJsonl({
        type: "message_update",
        message: { role: "assistant", content: [{ type: "text", text: `sdk prompt: ${message}` }] },
        assistantMessageEvent: { type: "text_delta", delta: message },
      });
      writeJsonl({
        type: "message_end",
        message: {
          role: "assistant",
          stopReason: "end_turn",
          content: [{ type: "text", text: `sdk prompt: ${message}` }],
          usage: { input: 3, output: 4, cacheRead: 0, cacheWrite: 0, totalTokens: 7 },
        },
      });
      writeJsonl({ type: "context_window", totalTokens: 7, modelContextWindow: 100 });
      writeJsonl({ type: "agent_end", willRetry: false });
    },
    answerExtensionUI: async function (request) {
      throw new ProbeError("E_PARAM", `unknown extension UI request id: ${request.id}`);
    },
    dispose: async function () {},
  };
}

function createJsonlMessageEndOnlyRuntime() {
  return {
    kind: "message-end-only",
    prompt: async function (request) {
      const message = String(request.message ?? "").trim();
      if (!message) {
        throw new ProbeError("E_PARAM", "prompt message required");
      }
      writeJsonl(successResponse("prompt", { scenario: "message-end-only" }, request.id));
      writeJsonl({ type: "agent_start" });
      writeJsonl({ type: "message_start", message: { role: "assistant", content: [] } });
      writeJsonl({
        type: "message_update",
        message: { role: "assistant", content: [{ type: "text", text: `sdk prompt: ${message}` }] },
        assistantMessageEvent: { type: "text_delta", delta: `sdk prompt: ${message}` },
      });
      writeJsonl({
        type: "message_end",
        message: {
          role: "assistant",
          stopReason: "end_turn",
          content: [{ type: "text", text: `sdk prompt: ${message}` }],
          usage: { input: 3, output: 4, cacheRead: 0, cacheWrite: 0, totalTokens: 7 },
        },
      });
      writeJsonl({ type: "context_window", totalTokens: 7, modelContextWindow: 100 });
    },
    answerExtensionUI: async function (request) {
      throw new ProbeError("E_PARAM", `unknown extension UI request id: ${request.id}`);
    },
    dispose: async function () {},
  };
}

function createJsonlToolRuntime() {
  return {
    kind: "tool-events",
    prompt: async function (request) {
      const message = String(request.message ?? "").trim();
      if (!message) {
        throw new ProbeError("E_PARAM", "prompt message required");
      }
      writeJsonl(successResponse("prompt", { scenario: "tool-events" }, request.id));
      writeJsonl({ type: "agent_start" });
      writeJsonl({ type: "tool_execution_start", toolCallId: "tool-1", toolName: "ls", args: { path: "." } });
      writeJsonl({
        type: "tool_execution_update",
        toolCallId: "tool-1",
        toolName: "ls",
        args: { path: "." },
        partialResult: { content: [{ type: "text", text: "AGENTS.md" }] },
      });
      writeJsonl({
        type: "tool_execution_end",
        toolCallId: "tool-1",
        toolName: "ls",
        result: { content: [{ type: "text", text: "README.md" }] },
        isError: false,
      });
      writeJsonl({
        type: "message_end",
        message: {
          role: "assistant",
          stopReason: "end_turn",
          content: [{ type: "text", text: "listed files" }],
          usage: { input: 1, output: 2, totalTokens: 3 },
        },
      });
      writeJsonl({ type: "context_window", totalTokens: 3, modelContextWindow: 100 });
      writeJsonl({ type: "agent_end", willRetry: false });
    },
    answerExtensionUI: async function (request) {
      throw new ProbeError("E_PARAM", `unknown extension UI request id: ${request.id}`);
    },
    dispose: async function () {},
  };
}

function createJsonlTurnEndOnlyRuntime() {
  return {
    kind: "turn-end-only",
    prompt: async function (request) {
      const message = String(request.message ?? "").trim();
      if (!message) {
        throw new ProbeError("E_PARAM", "prompt message required");
      }
      writeJsonl(successResponse("prompt", { scenario: "turn-end-only" }, request.id));
      writeJsonl({ type: "agent_start" });
      writeJsonl({ type: "turn_end", stopReason: "end_turn", willRetry: false });
    },
    answerExtensionUI: async function (request) {
      throw new ProbeError("E_PARAM", `unknown extension UI request id: ${request.id}`);
    },
    dispose: async function () {},
  };
}

function createJsonlSlashRuntime() {
  const commands = [
    { name: "jira", description: "Jira issue lookup", source: "extension" },
    { name: "skill:jira", description: "Jira skill workflow", source: "skill" },
    { name: "review", description: "Review current changes", source: "prompt" },
  ];
  return {
    kind: "slash-controls",
    prompt: async function (request) {
      const message = String(request.message ?? "").trim();
      if (!message.startsWith("/")) {
        throw new ProbeError("E_PARAM", "slash-controls runtime supports only slash prompts");
      }
      writeJsonl(successResponse("prompt", { scenario: "slash-controls" }, request.id));
      writeJsonl({ type: "agent_start" });
      writeJsonl({ type: "message_start", message: { role: "assistant", content: [] } });
      writeJsonl({
        type: "message_update",
        message: { role: "assistant", content: [{ type: "text", text: `slash command executed: ${message}` }] },
        assistantMessageEvent: { type: "text_delta", delta: `slash command executed: ${message}` },
      });
      writeJsonl({
        type: "message_end",
        message: {
          role: "assistant",
          stopReason: "end_turn",
          content: [{ type: "text", text: `slash command executed: ${message}` }],
          usage: { input: 2, output: 3, totalTokens: 5 },
        },
      });
      writeJsonl({ type: "context_window", totalTokens: 5, modelContextWindow: 100 });
      writeJsonl({ type: "agent_end", willRetry: false });
    },
    getCommands: async function (request) {
      writeJsonl(successResponse("get_commands", { commands }, request.id));
    },
    answerExtensionUI: async function (request) {
      throw new ProbeError("E_PARAM", `unknown extension UI request id: ${request.id}`);
    },
    dispose: async function () {},
  };
}

function createJsonlControlsRuntime() {
  const models = [
    { id: "model", name: "Fake Model", provider: "fake", reasoning: true, thinkingLevelMap: { off: "off", high: "high" } },
    { id: "plain", name: "Plain Model", provider: "fake", reasoning: false },
  ];
  const state = {
    sessionId: "sdk-test",
    model: { provider: "fake", id: "model" },
    thinkingLevel: "off",
    isStreaming: false,
  };
  return {
    kind: "runtime-controls",
    prompt: async function (request) {
      const message = String(request.message ?? "").trim();
      if (!message) {
        throw new ProbeError("E_PARAM", "prompt message required");
      }
      writeJsonl(successResponse("prompt", { scenario: "runtime-controls" }, request.id));
      writeJsonl({ type: "agent_start" });
      if (message === "wait-for-cancel") {
        state.isStreaming = true;
        writeJsonl({ type: "message_start", message: { role: "assistant", content: [] } });
        writeJsonl({
          type: "message_update",
          message: { role: "assistant", content: [{ type: "text", text: "waiting for cancel" }] },
          assistantMessageEvent: { type: "text_delta", delta: "waiting for cancel" },
        });
        return;
      }
      writeJsonl({
        type: "message_end",
        message: {
          role: "assistant",
          stopReason: "end_turn",
          content: [{ type: "text", text: `runtime controls: ${message}` }],
          usage: { input: 1, output: 1, totalTokens: 2 },
        },
      });
      writeJsonl({ type: "agent_end", willRetry: false });
    },
    getState: async function (request) {
      writeJsonl(successResponse("get_state", state, request.id));
    },
    getAvailableModels: async function (request) {
      writeJsonl(successResponse("get_available_models", { models }, request.id));
    },
    setModel: async function (request) {
      const model = models.find((item) => item.provider === request.provider && item.id === request.modelId);
      if (!model) {
        throw new ProbeError("E_PARAM", `model not found: ${request.provider}/${request.modelId}`);
      }
      state.model = { provider: model.provider, id: model.id };
      writeJsonl(successResponse("set_model", model, request.id));
    },
    setThinkingLevel: async function (request) {
      const level = String(request.level ?? "").trim();
      if (!level) {
        throw new ProbeError("E_PARAM", "thinking level required");
      }
      state.thinkingLevel = level;
      writeJsonl(successResponse("set_thinking_level", { level }, request.id));
    },
    getCommands: async function (request) {
      writeJsonl(successResponse("get_commands", { commands: [] }, request.id));
    },
    abort: async function (request) {
      state.isStreaming = false;
      writeJsonl(successResponse("abort", { aborted: true }, request.id));
      writeJsonl({
        type: "message_end",
        message: {
          role: "assistant",
          stopReason: "aborted",
          content: [],
          errorMessage: "pi sdk prompt aborted",
        },
      });
      writeJsonl({ type: "agent_end", willRetry: false });
    },
    answerExtensionUI: async function (request) {
      throw new ProbeError("E_PARAM", `unknown extension UI request id: ${request.id}`);
    },
    dispose: async function () {},
  };
}

function createJsonlAbortHangsRuntime() {
  return {
    kind: "abort-hangs",
    prompt: async function (request) {
      const message = String(request.message ?? "").trim();
      if (!message) {
        throw new ProbeError("E_PARAM", "prompt message required");
      }
      writeJsonl(successResponse("prompt", { scenario: "abort-hangs" }, request.id));
      writeJsonl({ type: "agent_start" });
      writeJsonl({ type: "message_start", message: { role: "assistant", content: [] } });
      writeJsonl({
        type: "message_update",
        message: { role: "assistant", content: [{ type: "text", text: "waiting for abort" }] },
        assistantMessageEvent: { type: "text_delta", delta: "waiting for abort" },
      });
    },
    abort: async function () {
      writeJsonl({ type: "turn_end", stopReason: "aborted", willRetry: false });
    },
    answerExtensionUI: async function (request) {
      throw new ProbeError("E_PARAM", `unknown extension UI request id: ${request.id}`);
    },
    dispose: async function () {},
  };
}

async function createJsonlSDKRuntime(options) {
  const cwd = options.cwd ?? DEFAULT_CWD;
  const agentDir = options.agentDir ?? DEFAULT_AGENT_DIR;
  const scratch = await mkdtemp(join(tmpdir(), "mindfs-pi-sdk-jsonl-"));
  const sessionManager = SessionManager.create(cwd, join(scratch, "sessions"));
  const services = await createAgentSessionServices({
    cwd,
    agentDir,
  });
  const model = resolveJsonlModel(services.modelRegistry, options.model);
  const { session } = await createAgentSessionFromServices({
    services,
    sessionManager,
    model,
    thinkingLevel: "off",
  });
  let unsubscribe = session.subscribe((event) => {
    if (event.type === "agent_end") {
      writeJsonl(contextWindowEnvelope(session));
    }
    writeJsonl(event);
  });
  return {
    kind: "sdk",
    cwd,
    agentDir,
    pending: new Map(),
    responses: [],
    prompt: async function (request) {
      let preflightSucceeded = false;
      void session
        .prompt(String(request.message ?? ""), {
          source: "rpc",
          preflightResult: (didSucceed) => {
            if (didSucceed) {
              preflightSucceeded = true;
              writeJsonl(successResponse("prompt", { runtime: "sdk" }, request.id));
            }
          },
        })
        .catch((error) => {
          if (!preflightSucceeded) {
            writeJsonl(errorResponse("prompt", error, request.id));
            return;
          }
          writeJsonl({ type: "recovery", message: error instanceof Error ? error.message : String(error) });
          writeJsonl(contextWindowEnvelope(session));
          writeJsonl({ type: "agent_end", willRetry: false });
        });
    },
    answerExtensionUI: async function (request) {
      throw new ProbeError("E_PARAM", `unknown extension UI request id: ${request.id}`);
    },
    getState: async function (request) {
      writeJsonl(successResponse("get_state", {
        model: session.model,
        thinkingLevel: session.thinkingLevel,
        isStreaming: session.isStreaming,
        isCompacting: session.isCompacting,
        steeringMode: session.steeringMode,
        followUpMode: session.followUpMode,
        sessionFile: session.sessionFile,
        sessionId: session.sessionId,
        sessionName: session.sessionName,
        autoCompactionEnabled: session.autoCompactionEnabled,
        messageCount: session.messages?.length ?? 0,
        pendingMessageCount: session.pendingMessageCount,
      }, request.id));
    },
    getAvailableModels: async function (request) {
      const models = await session.modelRegistry.getAvailable();
      writeJsonl(successResponse("get_available_models", { models }, request.id));
    },
    setModel: async function (request) {
      const models = await session.modelRegistry.getAvailable();
      const model = models.find((item) => item.provider === request.provider && item.id === request.modelId);
      if (!model) {
        throw new ProbeError("E_PARAM", `model not found: ${request.provider}/${request.modelId}`);
      }
      await session.setModel(model);
      writeJsonl(successResponse("set_model", model, request.id));
    },
    setThinkingLevel: async function (request) {
      session.setThinkingLevel(request.level);
      writeJsonl(successResponse("set_thinking_level", { level: session.thinkingLevel }, request.id));
    },
    getCommands: async function (request) {
      const commands = [];
      for (const command of session.extensionRunner?.getRegisteredCommands?.() ?? []) {
        commands.push({
          name: command.invocationName,
          description: command.description,
          source: "extension",
          sourceInfo: command.sourceInfo,
        });
      }
      for (const template of session.promptTemplates ?? []) {
        commands.push({
          name: template.name,
          description: template.description,
          source: "prompt",
          sourceInfo: template.sourceInfo,
        });
      }
      for (const skill of session.resourceLoader?.getSkills?.().skills ?? []) {
        commands.push({
          name: `skill:${skill.name}`,
          description: skill.description,
          source: "skill",
          sourceInfo: skill.sourceInfo,
        });
      }
      writeJsonl(successResponse("get_commands", { commands }, request.id));
    },
    abort: async function (request) {
      await session.abort();
      writeJsonl(successResponse("abort", {}, request.id));
    },
    dispose: async function () {
      unsubscribe?.();
      unsubscribe = undefined;
      session.dispose?.();
      await rm(scratch, { recursive: true, force: true });
    },
  };
}

function resolveJsonlModel(modelRegistry, modelRef) {
  const ref = String(modelRef ?? "").trim();
  if (!ref) {
    return undefined;
  }
  const slash = ref.indexOf("/");
  if (slash <= 0 || slash === ref.length - 1) {
    throw new ProbeError("E_PARAM", "model must be provider/modelId");
  }
  const provider = ref.slice(0, slash);
  const modelId = ref.slice(slash + 1);
  const model = modelRegistry.find(provider, modelId);
  if (!model) {
    throw new ProbeError("E_PARAM", `model not found: ${ref}`);
  }
  return model;
}

function contextWindowEnvelope(session) {
  const usage = session.getContextUsage?.();
  return {
    type: "context_window",
    totalTokens: Number(usage?.tokens ?? 0) || 0,
    modelContextWindow: Number(usage?.contextWindow ?? session.state?.model?.contextWindow ?? 0) || 0,
  };
}

function buildJsonlUIEvents() {
  return [
    { type: "extension_ui_request", id: "notify-1", method: "notify", message: "ui-demo notification", notificationType: "info" },
    { type: "extension_ui_request", id: "status-1", method: "setStatus", statusKey: "mindfs.pi_sdk_bridge", statusText: "running" },
    { type: "extension_ui_request", id: "widget-1", method: "setWidget", widgetKey: "mindfs.pi_sdk_bridge", content: ["SDK bridge widget"], placement: "aboveEditor" },
    { type: "extension_ui_request", id: "title-1", method: "setTitle", title: "MindFS Pi SDK Bridge Smoke" },
    { type: "extension_ui_request", id: "editor-text-1", method: "set_editor_text", text: "prefilled by ui-demo" },
    { type: "extension_ui_request", id: "select-1", method: "select", title: "Choose bridge route", options: ["rpc-first", "sdk-bridge"] },
    { type: "extension_ui_request", id: "confirm-1", method: "confirm", title: "Confirm SDK bridge", message: "Continue deterministic smoke?" },
    { type: "extension_ui_request", id: "input-1", method: "input", title: "Bridge input", placeholder: "type here" },
    { type: "extension_ui_request", id: "editor-1", method: "editor", title: "Bridge editor", prefill: "initial text" },
  ];
}

function sanitizeDialogOptions(options) {
  if (!options) {
    return undefined;
  }
  return {
    timeout: options.timeout,
    hasSignal: Boolean(options.signal),
  };
}

function sanitizeEntry(entry) {
  if (entry.type === "message") {
    return {
      type: entry.type,
      id: entry.id,
      parentId: entry.parentId,
      timestamp: entry.timestamp,
      role: entry.message?.role,
      textPreview: preview(messageText(entry.message)),
    };
  }
  if (entry.type === "custom") {
    return {
      type: entry.type,
      customType: entry.customType,
      id: entry.id,
      parentId: entry.parentId,
      timestamp: entry.timestamp,
      data: entry.data,
    };
  }
  return {
    type: entry.type,
    id: entry.id,
    parentId: entry.parentId,
    timestamp: entry.timestamp,
  };
}

function messageText(message) {
  const content = message?.content;
  if (typeof content === "string") {
    return content;
  }
  if (Array.isArray(content)) {
    return content.filter((part) => part.type === "text").map((part) => part.text).join(" ");
  }
  return "";
}

function defaultSessionDirPath(cwd, agentDir) {
  const safePath = `--${resolve(cwd).replace(/^[\\/]/, "").replace(/[\\/:]/g, "-")}--`;
  return join(resolve(agentDir), "sessions", safePath);
}

function countEntryTypes(entries) {
  const counts = {};
  for (const entry of entries) {
    counts[entry.type] = (counts[entry.type] ?? 0) + 1;
  }
  return counts;
}

function maxTreeDepth(nodes) {
  let maxDepth = 0;
  const visit = (node, depth) => {
    maxDepth = Math.max(maxDepth, depth);
    for (const child of node.children ?? []) {
      visit(child, depth + 1);
    }
  };
  for (const node of nodes) {
    visit(node, 1);
  }
  return maxDepth;
}

function makeIdGenerator(prefix) {
  let counter = 0;
  return (method) => `${prefix}-${method}-${++counter}`;
}

function limited(items, limit) {
  return items.slice(0, limit);
}

function safeTitle(text) {
  return preview(text, 120);
}

function preview(text, max = 160) {
  if (!text) {
    return "";
  }
  const oneLine = String(text).replace(/\s+/g, " ").trim();
  return oneLine.length > max ? `${oneLine.slice(0, max)}…` : oneLine;
}

process.on("unhandledRejection", (error) => {
  printJson(errorResponse("unhandledRejection", error));
  process.exitCode = 1;
});

await main();
