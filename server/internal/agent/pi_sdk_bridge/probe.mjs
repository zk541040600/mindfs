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
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
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
const SDK_PROMPT_IDLE_POLL_MS = 250;
const SDK_PROMPT_IDLE_FALLBACK_MS = 1500;
const SDK_PROMPT_HARD_IDLE_TIMEOUT_MS = 120000;
const SDK_AGENT_SETTLED_DRAIN_MS = 150;
const SDK_JSONL_EOF_PROMPT_GRACE_MS = 120000;
const MAX_GOAL_OBJECTIVE_BYTES = 4096;
const MAX_GOAL_DETAIL_BYTES = 2048;
const GOAL_STATE_STATUSES = new Set(["active", "paused", "complete"]);

const protocolStdoutWrite = process.stdout.write.bind(process.stdout);
const protocolStderrWrite = process.stderr.write.bind(process.stderr);
let protocolStdoutWriteDepth = 0;

function writeProtocolStdout(text) {
  protocolStdoutWriteDepth += 1;
  try {
    return protocolStdoutWrite(text);
  } finally {
    protocolStdoutWriteDepth -= 1;
  }
}

process.stdout.write = (chunk, encoding, callback) => {
  if (protocolStdoutWriteDepth > 0) {
    return protocolStdoutWrite(chunk, encoding, callback);
  }
  return protocolStderrWrite(chunk, encoding, callback);
};

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
  addCandidate(candidates, join(dirname(process.execPath), "..", "lib", "node_modules", packagePath));
  addCandidate(candidates, join(DEFAULT_AGENT_DIR, "npm", "node_modules", packagePath));

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
      const sdk = await import(toImportSpecifier(candidate));
      await takeOverPiStdout(candidate);
      return sdk;
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

async function takeOverPiStdout(candidate) {
  const candidatePath = String(candidate || "");
  const marker = "/dist/index.js";
  const markerIndex = candidatePath.endsWith(marker) ? candidatePath.length - marker.length : -1;
  if (markerIndex < 0) {
    return;
  }
  try {
    const guardPath = join(candidatePath.slice(0, markerIndex), "dist", "core", "output-guard.js");
    const guard = await import(pathToFileURL(guardPath).href);
    guard.takeOverStdout?.();
  } catch {
    // Older SDK builds may not expose the guard; keep the bridge-level stdout isolation above.
  }
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
    if (command === "jsonl") {
      try {
        await ensurePiSDK();
      } catch (error) {
        writeJsonl(errorResponse("jsonl", error));
        process.exitCode = 1;
        return;
      }
      await runJsonl(argv);
      return;
    }

    await ensurePiSDK();

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
  writeProtocolStdout(`${JSON.stringify(value, null, 2)}\n`);
}

function writeJsonl(value) {
  writeProtocolStdout(`${JSON.stringify(value)}\n`);
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
  const availableModels = await filterModelsByPiEnabledModels(modelRegistry.getAvailable(), options.agentDir);
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
    // Fork/clone are validated by runtime-replacement-smoke but are not yet
    // product controls exposed through the production JSONL runtime.
    fork: false,
    clone: false,
    importJsonl: true,
    compact: true,
    activeTools: true,
    queueModes: true,
    retryControls: true,
    resources: true,
    deterministicHarness: true,
    runtimeReplacementSmoke: true,
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

const PI_THINKING_LEVEL_SUFFIX = /:(?:off|minimal|low|medium|high|xhigh|max)$/;

async function readPiEnabledModelIDs(agentDir) {
  const settingsPath = join(agentDir || DEFAULT_AGENT_DIR, "settings.json");
  try {
    const settings = JSON.parse(await readFile(settingsPath, "utf8"));
    const enabledModels = Array.isArray(settings?.enabledModels) ? settings.enabledModels : [];
    return Array.from(new Set(enabledModels
      .map((item) => String(item || "").trim().replace(PI_THINKING_LEVEL_SUFFIX, ""))
      .filter(Boolean)));
  } catch (error) {
    console.warn(`[mindfs/pi] failed to read enabledModels from ${settingsPath}: ${error instanceof Error ? error.message : String(error)}`);
    return [];
  }
}

async function filterModelsByPiEnabledModels(models, agentDir) {
  const available = Array.isArray(models) ? models : [];
  const enabledModelIDs = await readPiEnabledModelIDs(agentDir);
  if (enabledModelIDs.length === 0 || available.length === 0) {
    return available;
  }
  const byID = new Map();
  for (const model of available) {
    const fullID = `${String(model?.provider || "").trim()}/${String(model?.id || "").trim()}`;
    if (fullID !== "/") {
      byID.set(fullID, model);
    }
  }
  const filtered = [];
  for (const fullID of enabledModelIDs) {
    const model = byID.get(fullID);
    if (model) {
      filtered.push(model);
    }
  }
  // Safety fallback: if the installed Pi registry and settings are temporarily out of sync,
  // keep the original list instead of rendering an empty model dropdown.
  return filtered.length > 0 ? filtered : available;
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

function truncateUTF8Text(value, maxBytes) {
  if (maxBytes <= 0 || !value) {
    return "";
  }
  if (Buffer.byteLength(value, "utf8") <= maxBytes) {
    return value;
  }
  let result = "";
  let bytes = 0;
  for (const character of value) {
    const nextBytes = Buffer.byteLength(character, "utf8");
    if (bytes + nextBytes > maxBytes) {
      break;
    }
    result += character;
    bytes += nextBytes;
  }
  return result;
}

function sanitizeGoalText(value, maxBytes) {
  const sanitized = sanitizeTranscriptText(typeof value === "string" ? value : "");
  if (sanitized.binaryLike) {
    return "";
  }
  return truncateUTF8Text(sanitized.text, maxBytes);
}

function normalizeGoalMetric(value) {
  return typeof value === "number" && Number.isFinite(value) && value >= 0
    ? Math.min(value, Number.MAX_SAFE_INTEGER)
    : 0;
}

function normalizeGoalStateEntry(entry) {
  if (entry?.type !== "custom" || entry.customType !== "pi-goal-state") {
    return undefined;
  }
  const goal = entry.data?.goal;
  const status = typeof goal?.status === "string" ? goal.status.trim().toLowerCase() : "";
  if (!goal || !GOAL_STATE_STATUSES.has(status)) {
    return undefined;
  }
  return {
    objective: sanitizeGoalText(goal.objective, MAX_GOAL_OBJECTIVE_BYTES),
    status,
    autoContinue: goal.autoContinue === true,
    updatedAt: normalizeTimestamp(goal.updatedAt ?? entry.timestamp) ?? "",
    usage: {
      tokensUsed: normalizeGoalMetric(goal.usage?.tokensUsed),
      activeSeconds: normalizeGoalMetric(goal.usage?.activeSeconds),
    },
    pauseReason: sanitizeGoalText(goal.pauseReason, MAX_GOAL_DETAIL_BYTES),
    pauseSuggestedAction: sanitizeGoalText(goal.pauseSuggestedAction, MAX_GOAL_DETAIL_BYTES),
    stopReason: sanitizeGoalText(goal.stopReason, MAX_GOAL_DETAIL_BYTES),
  };
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
    theme: noopTheme(),
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
  let eofExitTimer;
  const scheduleEofExit = () => {
    if (!process.stdin.readableEnded && !process.stdin.destroyed) {
      return;
    }
    if (eofExitTimer) {
      clearTimeout(eofExitTimer);
    }
    const startedAt = Date.now();
    const attemptExit = () => {
      if (runtime?.isPromptActive?.() && Date.now() - startedAt < SDK_JSONL_EOF_PROMPT_GRACE_MS) {
        eofExitTimer = setTimeout(attemptExit, 500);
        eofExitTimer.unref?.();
        return;
      }
      void runtime?.dispose?.();
      process.exit(0);
    };
    eofExitTimer = setTimeout(attemptExit, 500);
    eofExitTimer.unref?.();
  };
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
          runtime = createJsonlTestRuntime(request.scenario, resolve(request.agentDir ?? baseOptions.agentDir));
          writeJsonl({ id: request.id, type: "response", command: "start_test_runtime", success: true, data: { scenario: request.scenario } });
        } else if (request.type === "start_sdk_runtime") {
          await runtime?.dispose?.();
          runtime = await createJsonlSDKRuntime({ ...baseOptions, cwd: resolve(request.cwd ?? baseOptions.cwd), agentDir: resolve(request.agentDir ?? baseOptions.agentDir), model: request.model, mode: request.mode, sessionId: request.sessionId });
          writeJsonl({
            id: request.id,
            type: "response",
            command: "start_sdk_runtime",
            success: true,
            data: {
              cwd: runtime.cwd,
              agentDir: runtime.agentDir,
              sessionDir: runtime.sessionDir,
              sessionId: runtime.sessionId,
              sessionFile: runtime.sessionFile,
              resumed: runtime.resumed,
              model: runtime.model,
              thinkingLevel: runtime.thinkingLevel,
              extensionsReady: runtime.extensionsReady,
              extensionsFailed: runtime.extensionsFailed,
              extensionErrorCount: runtime.extensionErrors.length,
              extensionErrors: runtime.extensionErrors,
            },
          });
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
      scheduleEofExit();
    }
  } finally {
    if (eofExitTimer) {
      clearTimeout(eofExitTimer);
    }
    await runtime?.dispose?.();
    process.exit(0);
  }
}

async function dispatchJsonlRuntimeControl(runtime, request) {
  const handlers = {
    get_state: "getState",
    get_available_models: "getAvailableModels",
    set_model: "setModel",
    set_thinking_level: "setThinkingLevel",
    get_commands: "getCommands",
    steer: "steer",
    follow_up: "followUp",
    followup: "followUp",
    compact: "compact",
    abort_compaction: "abortCompaction",
    get_active_tools: "getActiveTools",
    get_all_tools: "getAllTools",
    set_active_tools: "setActiveTools",
    set_queue_modes: "setQueueModes",
    set_auto_compaction: "setAutoCompaction",
    set_auto_retry: "setAutoRetry",
    abort_retry: "abortRetry",
    answer_question: "answerQuestion",
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

function createJsonlTestRuntime(scenario, agentDir) {
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
  if (scenario === "ask-user-todo") {
    return createJsonlAskUserTodoRuntime();
  }
  if (scenario === "tool-multi-turn") {
    return createJsonlToolMultiTurnRuntime();
  }
  if (scenario === "text-then-delayed-tool") {
    return createJsonlTextThenDelayedToolRuntime();
  }
  if (scenario === "agent-end-retry") {
    return createJsonlAgentEndRetryRuntime();
  }
  if (scenario === "prompt-done-after-agent-end") {
    return createJsonlPromptDoneAfterAgentEndRuntime();
  }
  if (scenario === "goal-state-settled") {
    return createJsonlGoalStateRuntime();
  }
  if (scenario === "turn-end-only") {
    return createJsonlTurnEndOnlyRuntime();
  }
  if (scenario === "slash-controls") {
    return createJsonlSlashRuntime();
  }
  if (scenario === "runtime-controls") {
    return createJsonlControlsRuntime(agentDir);
  }
  if (scenario === "runtime-busy-controls") {
    return createJsonlControlsRuntime(agentDir, true);
  }
  if (scenario === "abort-hangs") {
    return createJsonlAbortHangsRuntime();
  }
  throw new ProbeError("E_PARAM", `unsupported test runtime scenario: ${scenario}`);
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
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

function createJsonlAskUserTodoRuntime() {
  const runtime = {
    kind: "ask-user-todo",
    pendingQuestions: new Map(),
    prompt: async function (request) {
      const message = String(request.message ?? "").trim();
      if (!message) {
        throw new ProbeError("E_PARAM", "prompt message required");
      }
      writeJsonl(successResponse("prompt", { scenario: "ask-user-todo" }, request.id));
      writeJsonl({ type: "agent_start" });
      writeJsonl({
        type: "tool_execution_start",
        toolCallId: "todo-1",
        toolName: "todo_write",
        args: {
          todos: [
            { content: "Audit Pi SDK bridge", activeForm: "Auditing Pi SDK bridge", status: "in_progress" },
            { content: "Wire ask user", activeForm: "Wiring ask user", status: "pending" },
          ],
        },
      });
      writeJsonl({
        type: "tool_execution_end",
        toolCallId: "todo-1",
        toolName: "todo_write",
        args: {
          todos: [
            { content: "Audit Pi SDK bridge", activeForm: "Auditing Pi SDK bridge", status: "completed" },
            { content: "Wire ask user", activeForm: "Wiring ask user", status: "in_progress" },
          ],
        },
        result: { content: [{ type: "text", text: "Todos updated" }] },
        isError: false,
      });
      writeJsonl({
        type: "tool_execution_start",
        toolCallId: "ask-1",
        toolName: "ask_user_question",
        args: {
          questions: [
            {
              question: "Which bridge route should MindFS use?",
              header: "Route",
              multiSelect: false,
              options: [
                { label: "SDK bridge", description: "Use Pi SDK runtime" },
                { label: "RPC fallback", description: "Use legacy RPC runtime" },
              ],
            },
          ],
        },
      });
      runtime.pendingQuestions.set("ask-1", { promptId: request.id });
    },
    answerQuestion: async function (request) {
      const toolUseId = String(request.toolUseId ?? "").trim();
      const pending = runtime.pendingQuestions.get(toolUseId);
      if (!pending) {
        throw new ProbeError("E_PARAM", `unknown ask user question id: ${toolUseId || request.toolUseId}`);
      }
      runtime.pendingQuestions.delete(toolUseId);
      const answers = request.answers && typeof request.answers === "object" ? request.answers : {};
      const answerText = Object.values(answers).map((value) => String(value)).filter(Boolean).join(", ");
      writeJsonl(successResponse("answer_question", { toolUseId, remaining: runtime.pendingQuestions.size }, request.id));
      writeJsonl({
        type: "tool_execution_end",
        toolCallId: toolUseId,
        toolName: "ask_user_question",
        args: {
          questions: [
            {
              question: "Which bridge route should MindFS use?",
              header: "Route",
              multiSelect: false,
              options: [
                { label: "SDK bridge", description: "Use Pi SDK runtime" },
                { label: "RPC fallback", description: "Use legacy RPC runtime" },
              ],
            },
          ],
        },
        result: { content: [{ type: "text", text: `Answer received: ${answerText}` }], details: { answers } },
        isError: false,
      });
      writeJsonl({
        type: "message_update",
        message: { role: "assistant", content: [{ type: "text", text: `answer received: ${answerText}` }] },
        assistantMessageEvent: { type: "text_delta", delta: `answer received: ${answerText}` },
      });
      writeJsonl({
        type: "message_end",
        message: {
          role: "assistant",
          stopReason: "end_turn",
          content: [{ type: "text", text: `answer received: ${answerText}` }],
          usage: { input: 4, output: 5, cacheRead: 0, cacheWrite: 0, totalTokens: 9 },
        },
      });
      writeJsonl({ type: "context_window", totalTokens: 9, modelContextWindow: 100 });
      writeJsonl({ type: "agent_end", willRetry: false });
    },
    answerExtensionUI: async function (request) {
      throw new ProbeError("E_PARAM", `unknown extension UI request id: ${request.id}`);
    },
    dispose: async function () {
      runtime.pendingQuestions.clear();
    },
  };
  return runtime;
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

function createJsonlToolMultiTurnRuntime() {
  return {
    kind: "tool-multi-turn",
    prompt: async function (request) {
      const message = String(request.message ?? "").trim();
      if (!message) {
        throw new ProbeError("E_PARAM", "prompt message required");
      }
      writeJsonl(successResponse("prompt", { scenario: "tool-multi-turn" }, request.id));
      writeJsonl({ type: "agent_start" });
      writeJsonl({ type: "message_start", message: { role: "assistant", content: [] } });
      writeJsonl({
        type: "message_end",
        message: {
          role: "assistant",
          stopReason: "end_turn",
          content: [{ type: "toolCall", id: "tool-1", name: "ls", arguments: { path: "." } }],
          usage: { input: 2, output: 1, totalTokens: 3 },
        },
      });
      writeJsonl({ type: "tool_execution_start", toolCallId: "tool-1", toolName: "ls", args: { path: "." } });
      writeJsonl({
        type: "tool_execution_end",
        toolCallId: "tool-1",
        toolName: "ls",
        result: { content: [{ type: "text", text: "README.md" }] },
        isError: false,
      });
      writeJsonl({ type: "turn_end", stopReason: "end_turn", willRetry: false });
      await delay(SDK_PROMPT_IDLE_FALLBACK_MS + 200);
      writeJsonl({ type: "turn_start" });
      writeJsonl({ type: "message_start", message: { role: "assistant", content: [] } });
      writeJsonl({
        type: "message_update",
        message: { role: "assistant", content: [{ type: "text", text: `final answer after tool: ${message}` }] },
        assistantMessageEvent: { type: "text_delta", delta: `final answer after tool: ${message}` },
      });
      writeJsonl({
        type: "message_end",
        message: {
          role: "assistant",
          stopReason: "end_turn",
          content: [{ type: "text", text: `final answer after tool: ${message}` }],
          usage: { input: 4, output: 5, totalTokens: 9 },
        },
      });
      writeJsonl({ type: "context_window", totalTokens: 9, modelContextWindow: 100 });
      writeJsonl({ type: "agent_end", willRetry: false });
    },
    answerExtensionUI: async function (request) {
      throw new ProbeError("E_PARAM", `unknown extension UI request id: ${request.id}`);
    },
    dispose: async function () {},
  };
}

function createJsonlTextThenDelayedToolRuntime() {
  return {
    kind: "text-then-delayed-tool",
    prompt: async function (request) {
      const message = String(request.message ?? "").trim();
      if (!message) {
        throw new ProbeError("E_PARAM", "prompt message required");
      }
      writeJsonl(successResponse("prompt", { scenario: "text-then-delayed-tool" }, request.id));
      writeJsonl({ type: "agent_start" });
      writeJsonl({ type: "message_start", message: { role: "assistant", content: [] } });
      writeJsonl({
        type: "message_update",
        message: { role: "assistant", content: [{ type: "text", text: `preparing tool: ${message}` }] },
        assistantMessageEvent: { type: "text_delta", delta: `preparing tool: ${message}` },
      });
      writeJsonl({
        type: "message_end",
        message: {
          role: "assistant",
          stopReason: "end_turn",
          content: [{ type: "text", text: `preparing tool: ${message}` }],
          usage: { input: 2, output: 2, totalTokens: 4 },
        },
      });
      await delay(SDK_PROMPT_IDLE_FALLBACK_MS + 750);
      writeJsonl({ type: "tool_execution_start", toolCallId: "tool-1", toolName: "execute bash", args: { command: "pwd" } });
      writeJsonl({
        type: "tool_execution_end",
        toolCallId: "tool-1",
        toolName: "execute bash",
        result: { content: [{ type: "text", text: "/root/mindfs" }] },
        isError: false,
      });
      writeJsonl({ type: "turn_end", stopReason: "end_turn", willRetry: false });
      writeJsonl({ type: "message_start", message: { role: "assistant", content: [] } });
      writeJsonl({
        type: "message_update",
        message: { role: "assistant", content: [{ type: "text", text: `delayed tool complete: ${message}` }] },
        assistantMessageEvent: { type: "text_delta", delta: `delayed tool complete: ${message}` },
      });
      writeJsonl({
        type: "message_end",
        message: {
          role: "assistant",
          stopReason: "end_turn",
          content: [{ type: "text", text: `delayed tool complete: ${message}` }],
          usage: { input: 3, output: 5, totalTokens: 8 },
        },
      });
      writeJsonl({ type: "context_window", totalTokens: 8, modelContextWindow: 100 });
      writeJsonl({ type: "agent_end", willRetry: false });
    },
    answerExtensionUI: async function (request) {
      throw new ProbeError("E_PARAM", `unknown extension UI request id: ${request.id}`);
    },
    dispose: async function () {},
  };
}

function createJsonlAgentEndRetryRuntime() {
  return {
    kind: "agent-end-retry",
    prompt: async function (request) {
      const message = String(request.message ?? "").trim();
      if (!message) {
        throw new ProbeError("E_PARAM", "prompt message required");
      }
      writeJsonl(successResponse("prompt", { scenario: "agent-end-retry" }, request.id));
      writeJsonl({ type: "agent_start" });
      writeJsonl({ type: "agent_end", willRetry: true });
      await delay(100);
      writeJsonl({ type: "message_start", message: { role: "assistant", content: [] } });
      writeJsonl({
        type: "message_update",
        message: { role: "assistant", content: [{ type: "text", text: `retry finished: ${message}` }] },
        assistantMessageEvent: { type: "text_delta", delta: `retry finished: ${message}` },
      });
      writeJsonl({
        type: "message_end",
        message: {
          role: "assistant",
          stopReason: "end_turn",
          content: [{ type: "text", text: `retry finished: ${message}` }],
          usage: { input: 1, output: 2, totalTokens: 3 },
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

function createJsonlPromptDoneAfterAgentEndRuntime() {
  return {
    kind: "prompt-done-after-agent-end",
    prompt: async function (request) {
      const message = String(request.message ?? "").trim();
      if (!message) {
        throw new ProbeError("E_PARAM", "prompt message required");
      }
      writeJsonl(successResponse("prompt", { scenario: "prompt-done-after-agent-end" }, request.id));
      writeJsonl({ type: "agent_start" });
      writeJsonl({ type: "message_start", message: { role: "assistant", content: [] } });
      writeJsonl({
        type: "message_update",
        message: { role: "assistant", content: [{ type: "text", text: `answer before compaction: ${message}` }] },
        assistantMessageEvent: { type: "text_delta", delta: `answer before compaction: ${message}` },
      });
      writeJsonl({
        type: "message_end",
        message: {
          role: "assistant",
          stopReason: "end_turn",
          content: [{ type: "text", text: `answer before compaction: ${message}` }],
          usage: { input: 80, output: 20, totalTokens: 100 },
        },
      });
      writeJsonl({ type: "agent_end", willRetry: false, promptDone: false });
      writeJsonl({ type: "compaction_start", reason: "threshold" });
      await delay(SDK_PROMPT_IDLE_FALLBACK_MS + 200);
      writeJsonl({
        type: "compaction_end",
        reason: "threshold",
        result: { summary: "summary", firstKeptEntryId: "entry-1", tokensBefore: 100 },
        aborted: false,
        willRetry: false,
      });
      writeJsonl({ type: "context_window", totalTokens: 40, modelContextWindow: 100 });
      writeJsonl({ type: "runtime_settled", reason: "test_runtime_settled" });
    },
    answerExtensionUI: async function (request) {
      throw new ProbeError("E_PARAM", `unknown extension UI request id: ${request.id}`);
    },
    dispose: async function () {},
  };
}

function createJsonlGoalStateRuntime() {
  return {
    kind: "goal-state-settled",
    prompt: async function (request) {
      writeJsonl(successResponse("prompt", { scenario: "goal-state-settled" }, request.id));
      const goalState = normalizeGoalStateEntry({
        type: "custom",
        customType: "pi-goal-state",
        timestamp: "2026-07-11T12:00:00Z",
        data: {
          goal: {
            objective: "repair session token=1234567890abcdef",
            status: "paused",
            autoContinue: false,
            updatedAt: "2026-07-11T12:00:00Z",
            usage: { tokensUsed: 42, activeSeconds: 7.5 },
            pauseReason: "waiting for approval password=1234567890abcdef",
            pauseSuggestedAction: "approve the next step",
            activePath: "/private/path/that/must/not/leave/the/bridge",
          },
          arbitraryPayload: { secret: "must-not-leak" },
        },
      });
      if (goalState) {
        writeJsonl({ type: "goal_state", ...goalState });
      }
      writeJsonl({ type: "runtime_settled", reason: "test_goal_state_settled" });
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

function createJsonlControlsRuntime(agentDir, initiallyBusy = false) {
  const models = [
    { id: "model", name: "Fake Model", provider: "fake", reasoning: true, thinkingLevelMap: { off: "off", high: "high" } },
    { id: "plain", name: "Plain Model", provider: "fake", reasoning: false },
  ];
  const allTools = [
    { name: "read", description: "Read a file", parameters: { type: "object" }, promptGuidelines: ["Read before edit"], sourceInfo: { source: "builtin", scope: "test" } },
    { name: "edit", description: "Edit a file", parameters: { type: "object" }, promptGuidelines: [], sourceInfo: { source: "builtin", scope: "test" } },
  ];
  const state = {
    sessionId: "sdk-test",
    model: { provider: "fake", id: "model" },
    thinkingLevel: "off",
    isStreaming: initiallyBusy,
    steering: [],
    followUp: [],
    steeringMode: "all",
    followUpMode: "all",
    activeTools: ["read"],
    autoCompactionEnabled: true,
    autoRetryEnabled: true,
    compactCount: 0,
    abortCompactionCount: 0,
    abortRetryCount: 0,
  };
  const emitQueueUpdate = () => writeJsonl({ type: "queue_update", steering: state.steering, followUp: state.followUp });
  const deterministicQueueState = () => ({
    pendingMessageCount: state.steering.length + state.followUp.length,
    steering: [...state.steering],
    followUp: [...state.followUp],
  });
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
      writeJsonl(successResponse("get_state", { ...state, pendingMessageCount: state.steering.length + state.followUp.length }, request.id));
    },
    getAvailableModels: async function (request) {
      const enabledModels = await filterModelsByPiEnabledModels(models, agentDir);
      writeJsonl(successResponse("get_available_models", { models: enabledModels }, request.id));
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
    steer: async function (request) {
      state.steering.push(jsonlRequestText(request, "steer"));
      emitQueueUpdate();
      writeJsonl(successResponse("steer", deterministicQueueState(), request.id));
    },
    followUp: async function (request) {
      const message = jsonlRequestText(request, "follow_up");
      state.followUp.push(message);
      emitQueueUpdate();
      writeJsonl(successResponse("follow_up", deterministicQueueState(), request.id));
      await delay(50);
      state.isStreaming = true;
      writeJsonl({ type: "agent_start" });
      writeJsonl({ type: "message_start", message: { role: "assistant", content: [] } });
      writeJsonl({
        type: "message_update",
        message: { role: "assistant", content: [{ type: "text", text: `follow up: ${message}` }] },
        assistantMessageEvent: { type: "text_delta", delta: `follow up: ${message}` },
      });
      writeJsonl({
        type: "message_end",
        message: {
          role: "assistant",
          stopReason: "end_turn",
          content: [{ type: "text", text: `follow up: ${message}` }],
          usage: { input: 1, output: 2, totalTokens: 3 },
        },
      });
      state.followUp.shift();
      state.isStreaming = false;
      emitQueueUpdate();
      writeJsonl({ type: "runtime_settled", reason: "test_follow_up_settled" });
    },
    compact: async function (request) {
      state.compactCount += 1;
      writeJsonl({ type: "compaction_start", reason: request.reason || "manual" });
      writeJsonl({ type: "compaction_end", reason: request.reason || "manual", aborted: false, willRetry: false, result: { summary: "deterministic compaction" } });
      writeJsonl(successResponse("compact", { compactCount: state.compactCount }, request.id));
    },
    abortCompaction: async function (request) {
      state.abortCompactionCount += 1;
      writeJsonl(successResponse("abort_compaction", { abortCompactionCount: state.abortCompactionCount }, request.id));
    },
    getActiveTools: async function (request) {
      writeJsonl(successResponse("get_active_tools", { toolNames: [...state.activeTools] }, request.id));
    },
    getAllTools: async function (request) {
      writeJsonl(successResponse("get_all_tools", { tools: allTools.map(sanitizeToolInfo) }, request.id));
    },
    setActiveTools: async function (request) {
      state.activeTools = jsonlStringArray(request.toolNames ?? request.tools, "toolNames");
      writeJsonl(successResponse("set_active_tools", { toolNames: [...state.activeTools] }, request.id));
    },
    setQueueModes: async function (request) {
      if (request.steeringMode !== undefined) {
        state.steeringMode = normalizeQueueMode(request.steeringMode, "steeringMode");
      }
      if (request.followUpMode !== undefined) {
        state.followUpMode = normalizeQueueMode(request.followUpMode, "followUpMode");
      }
      writeJsonl(successResponse("set_queue_modes", { steeringMode: state.steeringMode, followUpMode: state.followUpMode }, request.id));
    },
    setAutoCompaction: async function (request) {
      state.autoCompactionEnabled = jsonlBoolean(request.enabled, "enabled");
      writeJsonl(successResponse("set_auto_compaction", { enabled: state.autoCompactionEnabled }, request.id));
    },
    setAutoRetry: async function (request) {
      state.autoRetryEnabled = jsonlBoolean(request.enabled, "enabled");
      writeJsonl(successResponse("set_auto_retry", { enabled: state.autoRetryEnabled }, request.id));
    },
    abortRetry: async function (request) {
      state.abortRetryCount += 1;
      writeJsonl(successResponse("abort_retry", { abortRetryCount: state.abortRetryCount }, request.id));
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
      writeJsonl({ type: "agent_end", stopReason: "aborted", willRetry: false });
    },
    answerExtensionUI: async function (request) {
      throw new ProbeError("E_PARAM", `unknown extension UI request id: ${request.id}`);
    },
    dispose: async function () {},
  };
}

function createMindFSAskUserQuestionTool(pendingQuestions) {
  return {
    name: "ask_user_question",
    label: "Ask User Question",
    description: "Ask the MindFS user one or more structured questions and wait for their answer. Use this only when local evidence is exhausted or a product decision is required.",
    promptSnippet: "Ask the user a structured question and wait for an answer",
    executionMode: "sequential",
    parameters: {
      type: "object",
      properties: {
        questions: {
          type: "array",
          minItems: 1,
          items: {
            type: "object",
            properties: {
              question: { type: "string" },
              header: { type: "string" },
              multiSelect: { type: "boolean" },
              options: {
                type: "array",
                items: {
                  type: "object",
                  properties: {
                    label: { type: "string" },
                    description: { type: "string" },
                  },
                  required: ["label"],
                  additionalProperties: true,
                },
              },
            },
            required: ["question"],
            additionalProperties: true,
          },
        },
      },
      required: ["questions"],
      additionalProperties: true,
    },
    async execute(toolCallId, params, signal) {
      const questions = Array.isArray(params?.questions) ? params.questions : [];
      if (questions.length === 0) {
        return {
          content: [{ type: "text", text: "Error: questions required" }],
          details: { cancelled: true, error: "questions required" },
        };
      }
      return new Promise((resolve, reject) => {
        const cleanup = () => {
          pendingQuestions.delete(toolCallId);
          signal?.removeEventListener?.("abort", onAbort);
        };
        const onAbort = () => {
          cleanup();
          reject(new Error("Ask user question aborted"));
        };
        pendingQuestions.set(toolCallId, {
          questions,
          resolve: (answers) => {
            cleanup();
            resolve({
              content: [{ type: "text", text: formatAskUserAnswerResult(answers) }],
              details: { questions, answers },
            });
          },
          reject: (error) => {
            cleanup();
            reject(error);
          },
        });
        if (signal?.aborted) {
          onAbort();
          return;
        }
        signal?.addEventListener?.("abort", onAbort, { once: true });
      });
    },
  };
}

function normalizeAnswerMap(value) {
  const answers = {};
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return answers;
  }
  for (const [key, rawValue] of Object.entries(value)) {
    const normalizedKey = String(key ?? "").trim();
    const normalizedValue = String(rawValue ?? "").trim();
    if (normalizedKey && normalizedValue) {
      answers[normalizedKey] = normalizedValue;
    }
  }
  return answers;
}

function formatAskUserAnswerResult(answers) {
  const values = Object.entries(answers ?? {})
    .map(([key, value]) => `${key}: ${value}`)
    .filter(Boolean);
  return values.length > 0 ? `User answered:\n${values.join("\n")}` : "User answered.";
}

function jsonlRequestText(request, command) {
  const text = String(request.message ?? request.content ?? request.text ?? "").trim();
  if (!text) {
    throw new ProbeError("E_PARAM", `${command} message required`);
  }
  return text;
}

function jsonlStringArray(value, fieldName) {
  const source = Array.isArray(value) ? value : [];
  const items = source.map((item) => String(item ?? "").trim()).filter(Boolean);
  if (items.length === 0) {
    throw new ProbeError("E_PARAM", `${fieldName} required`);
  }
  return items;
}

function jsonlBoolean(value, fieldName) {
  if (typeof value === "boolean") {
    return value;
  }
  if (typeof value === "string") {
    const normalized = value.trim().toLowerCase();
    if (normalized === "true") {
      return true;
    }
    if (normalized === "false") {
      return false;
    }
  }
  throw new ProbeError("E_PARAM", `${fieldName} boolean required`);
}

function queueState(session) {
  return {
    pendingMessageCount: Number(session?.pendingMessageCount ?? 0) || 0,
    steering: Array.from(session?.getSteeringMessages?.() ?? []),
    followUp: Array.from(session?.getFollowUpMessages?.() ?? []),
  };
}

function sanitizeToolInfo(tool) {
  return {
    name: tool?.name,
    description: tool?.description,
    parameters: tool?.parameters,
    promptGuidelines: tool?.promptGuidelines,
    sourceInfo: sanitizeSourceInfo(tool?.sourceInfo),
  };
}

function sanitizeCompactionResult(result) {
  if (!result || typeof result !== "object") {
    return result;
  }
  return {
    ...result,
    summary: result.summary === undefined ? undefined : preview(String(result.summary), 4000),
    details: result.details === undefined ? undefined : result.details,
  };
}

function normalizeQueueMode(value, fieldName) {
  const mode = String(value ?? "").trim();
  if (mode === "all" || mode === "one-at-a-time") {
    return mode;
  }
  throw new ProbeError("E_PARAM", `${fieldName} must be all or one-at-a-time`);
}

function installExtensionEventForwarding(session) {
  const runner = session.extensionRunner;
  if (!runner) {
    return () => {};
  }
  const originalEmit = typeof runner.emit === "function" ? runner.emit.bind(runner) : undefined;
  if (originalEmit) {
    runner.emit = async (event) => {
      if (event?.type === "model_select" || event?.type === "thinking_level_select") {
        writeJsonl(sanitizeExtensionEvent(event));
      }
      return originalEmit(event);
    };
  }
  return () => {
    if (originalEmit) {
      runner.emit = originalEmit;
    }
  };
}

function sanitizeExtensionEvent(event) {
  if (event?.type === "model_select") {
    return {
      type: event.type,
      model: sanitizeModel(event.model),
      previousModel: sanitizeModel(event.previousModel),
      source: event.source,
    };
  }
  if (event?.type === "thinking_level_select") {
    return {
      type: event.type,
      level: event.level,
      previousLevel: event.previousLevel,
    };
  }
  return event;
}

async function createJsonlSDKRuntime(options) {
  const cwd = options.cwd ?? DEFAULT_CWD;
  const agentDir = options.agentDir ?? DEFAULT_AGENT_DIR;
  const thinkingLevel = typeof options.mode === "string" && options.mode.trim() ? options.mode.trim() : "off";
  const sessionState = await openJsonlSDKSessionManager(options, cwd, agentDir);
  const services = await createAgentSessionServices({
    cwd,
    agentDir,
  });
  const model = resolveJsonlModel(services.modelRegistry, options.model);
  const pendingQuestions = new Map();
  const { session } = await createAgentSessionFromServices({
    services,
    sessionManager: sessionState.sessionManager,
    model,
    thinkingLevel,
    customTools: [createMindFSAskUserQuestionTool(pendingQuestions)],
  });
  const restoreExtensionForwarding = installExtensionEventForwarding(session);
  const pendingUI = new Map();
  const extensionErrors = [];
  let bindingsSettled = false;
  let bindingsFailed = false;
  const bindingsReady = bindProbeExtensions(session, createJsonlBridgeUI(pendingUI), extensionErrors)
    .then(() => {
      bindingsSettled = true;
    })
    .catch((error) => {
      bindingsSettled = true;
      bindingsFailed = true;
      extensionErrors.push(error);
      writeJsonl({
        type: "recovery",
        message: `Pi SDK extension binding failed: ${error instanceof Error ? error.message : String(error)}`,
      });
    });
  let activePrompt = undefined;
  const clearPromptWatchdog = (state) => {
    if (state?.timer) {
      clearInterval(state.timer);
      state.timer = undefined;
    }
    if (state?.settleTimer) {
      clearTimeout(state.settleTimer);
      state.settleTimer = undefined;
    }
    if (activePrompt === state) {
      activePrompt = undefined;
    }
  };
  const messagesForSession = () => {
    if (Array.isArray(session.messages)) {
      return session.messages;
    }
    if (Array.isArray(session.state?.messages)) {
      return session.state.messages;
    }
    return [];
  };
  const createPromptState = (requestId, preflightSucceeded = false) => ({
    requestId,
    startedAt: Date.now(),
    lastActivityAt: Date.now(),
    baselineMessageCount: messagesForSession().length,
    sawAssistantActivity: false,
    seenMessageEnd: false,
    seenAgentEnd: false,
    preflightSucceeded,
    pendingToolCalls: new Set(),
    done: false,
    promptResolved: false,
    timer: undefined,
    settleTimer: undefined,
    settleReason: "",
  });
  const finishRuntimeSettled = (state, reason) => {
    if (!state || state.done) {
      return;
    }
    writeJsonl(contextWindowEnvelope(session));
    writeJsonl({ type: "runtime_settled", reason });
    state.done = true;
    clearPromptWatchdog(state);
  };
  const scheduleRuntimeSettled = (state, reason = "") => {
    if (!state || state.done) {
      return;
    }
    if (reason) {
      state.settleReason = reason;
    }
    if (state.settleTimer) {
      clearTimeout(state.settleTimer);
    }
    state.settleTimer = setTimeout(() => {
      state.settleTimer = undefined;
      finishRuntimeSettled(state, state.settleReason || "sdk_agent_settled");
    }, SDK_AGENT_SETTLED_DRAIN_MS);
  };
  const cancelScheduledSettlement = (state) => {
    if (!state?.settleTimer) {
      return;
    }
    clearTimeout(state.settleTimer);
    state.settleTimer = undefined;
  };
  const startPromptWatchdog = (state) => {
    state.timer = setInterval(() => {
      if (state.done) {
        clearPromptWatchdog(state);
        return;
      }
      const elapsed = Date.now() - (state.lastActivityAt ?? state.startedAt);
      if (elapsed < SDK_PROMPT_IDLE_FALLBACK_MS) {
        return;
      }
      if (session.isRetrying === true || session.isCompacting === true || state.pendingToolCalls.size > 0 || pendingUI.size > 0) {
        return;
      }
      if (elapsed < SDK_PROMPT_HARD_IDLE_TIMEOUT_MS) {
        return;
      }
      const error = new ProbeError("E_TIMEOUT", `Pi SDK prompt idle timeout after ${SDK_PROMPT_HARD_IDLE_TIMEOUT_MS}ms`);
      if (typeof session.abort === "function") {
        void session.abort().catch(() => {});
      }
      if (!state.preflightSucceeded) {
        writeJsonl(errorResponse("prompt", error, state.requestId));
        state.done = true;
        clearPromptWatchdog(state);
      } else {
        writeJsonl({ type: "recovery", message: error.message });
        finishRuntimeSettled(state, "sdk_prompt_idle_timeout");
      }
    }, SDK_PROMPT_IDLE_POLL_MS);
  };
  let unsubscribe = session.subscribe((event) => {
    const state = activePrompt;
    if (state && !state.done) {
      state.lastActivityAt = Date.now();
      if (event.type === "agent_settled") {
        scheduleRuntimeSettled(state, "sdk_agent_settled");
        return;
      }
      if (event.type === "entry_appended" && state.settleTimer) {
        scheduleRuntimeSettled(state);
      } else if (["agent_start", "turn_start", "message_start", "tool_execution_start"].includes(event.type)) {
        cancelScheduledSettlement(state);
      }
      if (event.type === "message_update") {
        state.sawAssistantActivity = true;
      } else if (event.type === "message_end") {
        state.seenMessageEnd = true;
        if (event.message?.role === "assistant") {
          state.sawAssistantActivity = true;
        }
      } else if (event.type === "tool_execution_start") {
        if (event.toolCallId) {
          state.pendingToolCalls.add(event.toolCallId);
        }
      } else if (event.type === "tool_execution_end") {
        if (event.toolCallId) {
          state.pendingToolCalls.delete(event.toolCallId);
        }
      } else if (event.type === "agent_end") {
        state.seenAgentEnd = true;
      }
    }
    if (event.type === "agent_end") {
      writeJsonl(contextWindowEnvelope(session));
      writeJsonl({ ...event, promptDone: false });
      return;
    }
    if (event.type === "entry_appended") {
      const goalState = normalizeGoalStateEntry(event.entry);
      if (goalState) {
        writeJsonl({ type: "goal_state", ...goalState });
      }
      return;
    }
    if (event.type === "agent_settled") {
      writeJsonl({ type: "runtime_settled", reason: "sdk_agent_settled" });
      return;
    }
    writeJsonl(event);
  });
  return {
    kind: "sdk",
    cwd,
    agentDir,
    sessionDir: sessionState.sessionDir,
    sessionId: session.sessionId,
    sessionFile: session.sessionFile,
    resumed: sessionState.resumed,
    get model() {
      return session.model;
    },
    get thinkingLevel() {
      return session.thinkingLevel;
    },
    get extensionsReady() {
      return bindingsSettled && !bindingsFailed;
    },
    get extensionsFailed() {
      return bindingsFailed;
    },
    get extensionErrors() {
      return extensionErrors.map((error) => error instanceof Error ? error.message : String(error));
    },
    pending: new Map(),
    responses: [],
    isPromptActive: function () {
      return Boolean(activePrompt && !activePrompt.done);
    },
    prompt: async function (request) {
      await bindingsReady;
      if (activePrompt && !activePrompt.done) {
        writeJsonl(errorResponse("prompt", new ProbeError("E_BUSY", "Agent is already processing. Use followUp to queue the message."), request.id));
        return;
      }
      const state = createPromptState(request.id);
      activePrompt = state;
      startPromptWatchdog(state);
      void session
        .prompt(String(request.message ?? ""), {
          source: "rpc",
          preflightResult: (didSucceed) => {
            if (didSucceed && !state.done && !state.preflightSucceeded) {
              state.preflightSucceeded = true;
              writeJsonl(successResponse("prompt", { runtime: "sdk" }, request.id));
            }
          },
        })
        .then(() => {
          if (state.done) {
            return;
          }
          if (!state.preflightSucceeded) {
            state.preflightSucceeded = true;
            writeJsonl(successResponse("prompt", { runtime: "sdk" }, request.id));
          }
          state.promptResolved = true;
          state.lastActivityAt = Date.now();
          if (String(request.message ?? "").trim().startsWith("/")) {
            scheduleRuntimeSettled(state, "sdk_slash_command_resolved");
          }
        })
        .catch((error) => {
          if (state.done) {
            return;
          }
          if (!state.preflightSucceeded) {
            state.done = true;
            state.seenAgentEnd = true;
            writeJsonl(errorResponse("prompt", error, request.id));
            clearPromptWatchdog(state);
            return;
          }
          writeJsonl({ type: "recovery", message: error instanceof Error ? error.message : String(error) });
          finishRuntimeSettled(state, "sdk_prompt_error");
        });
    },
    answerQuestion: async function (request) {
      const toolUseId = String(request.toolUseId ?? request.toolUseID ?? "").trim();
      const pending = pendingQuestions.get(toolUseId);
      if (!pending) {
        throw new ProbeError("E_PARAM", `unknown ask user question id: ${toolUseId || request.toolUseId}`);
      }
      const answers = normalizeAnswerMap(request.answers);
      if (Object.keys(answers).length === 0) {
        throw new ProbeError("E_PARAM", "answers required");
      }
      writeJsonl(successResponse("answer_question", { toolUseId, remaining: Math.max(0, pendingQuestions.size - 1) }, request.id));
      pending.resolve(answers);
    },
    answerExtensionUI: async function (request) {
      const id = String(request.id ?? "").trim();
      const pending = pendingUI.get(id);
      if (!pending) {
        throw new ProbeError("E_PARAM", `unknown extension UI request id: ${id || request.id}`);
      }
      const requestedMethod = String(request.method ?? "").trim();
      if (requestedMethod && requestedMethod !== pending.method) {
        throw new ProbeError("E_PARAM", `extension UI method mismatch for ${id}: got ${requestedMethod} want ${pending.method}`);
      }
      pendingUI.delete(id);
      pending.resolve(extensionUIResponseValue(pending.method, request));
      writeJsonl(successResponse("extension_ui_response", { method: pending.method, remaining: pendingUI.size }, id));
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
        extensionsReady: bindingsSettled && !bindingsFailed,
        extensionsFailed: bindingsFailed,
        extensionErrorCount: extensionErrors.length,
        extensionErrors: extensionErrors.map((error) => error instanceof Error ? error.message : String(error)),
      }, request.id));
    },
    getAvailableModels: async function (request) {
      const models = await filterModelsByPiEnabledModels(await session.modelRegistry.getAvailable(), agentDir);
      writeJsonl(successResponse("get_available_models", { models }, request.id));
    },
    setModel: async function (request) {
      const models = await filterModelsByPiEnabledModels(await session.modelRegistry.getAvailable(), agentDir);
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
      await bindingsReady;
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
    steer: async function (request) {
      await bindingsReady;
      await session.steer(jsonlRequestText(request, "steer"));
      writeJsonl(successResponse("steer", queueState(session), request.id));
    },
    followUp: async function (request) {
      await bindingsReady;
      if (!activePrompt || activePrompt.done) {
        activePrompt = createPromptState("", true);
        startPromptWatchdog(activePrompt);
      }
      await session.followUp(jsonlRequestText(request, "follow_up"));
      writeJsonl(successResponse("follow_up", queueState(session), request.id));
    },
    compact: async function (request) {
      await bindingsReady;
      const instructions = String(request.customInstructions ?? request.instructions ?? "").trim() || undefined;
      const result = await session.compact(instructions);
      writeJsonl(successResponse("compact", { result: sanitizeCompactionResult(result), contextUsage: session.getContextUsage?.() }, request.id));
    },
    abortCompaction: async function (request) {
      session.abortCompaction?.();
      writeJsonl(successResponse("abort_compaction", {}, request.id));
    },
    getActiveTools: async function (request) {
      writeJsonl(successResponse("get_active_tools", { toolNames: session.getActiveToolNames?.() ?? [] }, request.id));
    },
    getAllTools: async function (request) {
      writeJsonl(successResponse("get_all_tools", { tools: (session.getAllTools?.() ?? []).map(sanitizeToolInfo) }, request.id));
    },
    setActiveTools: async function (request) {
      const toolNames = jsonlStringArray(request.toolNames ?? request.tools, "toolNames");
      session.setActiveToolsByName?.(toolNames);
      writeJsonl(successResponse("set_active_tools", { toolNames: session.getActiveToolNames?.() ?? toolNames }, request.id));
    },
    setQueueModes: async function (request) {
      if (request.steeringMode !== undefined) {
        session.setSteeringMode?.(normalizeQueueMode(request.steeringMode, "steeringMode"));
      }
      if (request.followUpMode !== undefined) {
        session.setFollowUpMode?.(normalizeQueueMode(request.followUpMode, "followUpMode"));
      }
      writeJsonl(successResponse("set_queue_modes", { steeringMode: session.steeringMode, followUpMode: session.followUpMode }, request.id));
    },
    setAutoCompaction: async function (request) {
      const enabled = jsonlBoolean(request.enabled, "enabled");
      session.setAutoCompactionEnabled?.(enabled);
      writeJsonl(successResponse("set_auto_compaction", { enabled: session.autoCompactionEnabled }, request.id));
    },
    setAutoRetry: async function (request) {
      const enabled = jsonlBoolean(request.enabled, "enabled");
      session.setAutoRetryEnabled?.(enabled);
      writeJsonl(successResponse("set_auto_retry", { enabled: session.autoRetryEnabled }, request.id));
    },
    abortRetry: async function (request) {
      session.abortRetry?.();
      writeJsonl(successResponse("abort_retry", {}, request.id));
    },
    abort: async function (request) {
      await session.abort();
      writeJsonl(successResponse("abort", {}, request.id));
    },
    dispose: async function () {
      resolvePendingJsonlBridgeUI(pendingUI);
      for (const pending of pendingQuestions.values()) {
        pending.reject?.(new Error("Pi SDK runtime disposed"));
      }
      pendingQuestions.clear();
      restoreExtensionForwarding();
      unsubscribe?.();
      unsubscribe = undefined;
      session.dispose?.();
    },
  };
}

function createJsonlBridgeUI(pending) {
  const nextId = makeIdGenerator("ui");
  const requestDialog = (method, payload, options) =>
    new Promise((resolve) => {
      const id = nextId(method);
      pending.set(id, { method, resolve });
      writeJsonl({
        type: "extension_ui_request",
        id,
        method,
        ...payload,
        opts: sanitizeDialogOptions(options),
      });
    });
  const fireAndForget = (method, payload) => {
    writeJsonl({
      type: "extension_ui_request",
      id: nextId(method),
      method,
      ...payload,
    });
  };

  return {
    select: (title, choices, options) => requestDialog("select", { title, options: choices }, options),
    confirm: (title, message, options) => requestDialog("confirm", { title, message }, options),
    input: (title, placeholder, options) => requestDialog("input", { title, placeholder }, options),
    editor: (title, prefill, options) => requestDialog("editor", { title, prefill }, options),
    notify: (message, notificationType) => fireAndForget("notify", { message, notificationType }),
    onTerminalInput: () => () => {},
    setStatus: (statusKey, statusText) => fireAndForget("setStatus", { statusKey, statusText }),
    setWorkingMessage: (message) => fireAndForget("setWorkingMessage", { message }),
    setWorkingVisible: (visible) => fireAndForget("setWorkingVisible", { visible }),
    setWorkingIndicator: (options) => fireAndForget("setWorkingIndicator", { options }),
    setHiddenThinkingLabel: (label) => fireAndForget("setHiddenThinkingLabel", { label }),
    setWidget: (widgetKey, content, widgetOptions) => fireAndForget("setWidget", {
      widgetKey,
      content: typeof content === "function" ? "<component-factory>" : content,
      placement: widgetOptions?.placement,
      widgetPlacement: widgetOptions?.placement,
    }),
    setFooter: () => {},
    setHeader: () => {},
    setTitle: (title) => fireAndForget("setTitle", { title }),
    custom: async () => undefined,
    pasteToEditor: (text) => fireAndForget("pasteToEditor", { text }),
    setEditorText: (text) => fireAndForget("setEditorText", { text }),
    getEditorText: () => "",
    addAutocompleteProvider: () => {},
    setEditorComponent: () => {},
    getEditorComponent: () => undefined,
    theme: noopTheme(),
    getAllThemes: () => [],
    getTheme: () => undefined,
    setTheme: () => ({ success: false, error: "theme switching not implemented in MindFS bridge UI" }),
    getToolsExpanded: () => false,
    setToolsExpanded: () => {},
  };
}

function noopTheme() {
  const passthrough = (...args) => String(args[args.length - 1] ?? "");
  return {
    fg: passthrough,
    bg: passthrough,
    bold: passthrough,
    dim: passthrough,
    italic: passthrough,
    underline: passthrough,
    strikethrough: passthrough,
    inverse: passthrough,
  };
}

function extensionUIResponseValue(method, request) {
  if (request.cancelled) {
    return undefined;
  }
  if (method === "confirm") {
    return Boolean(request.confirmed);
  }
  if (request.value !== undefined) {
    return request.value;
  }
  if (request.text !== undefined) {
    return request.text;
  }
  return undefined;
}

function resolvePendingJsonlBridgeUI(pending) {
  for (const entry of pending.values()) {
    entry.resolve(undefined);
  }
  pending.clear();
}

async function openJsonlSDKSessionManager(options, cwd, agentDir) {
  const sessionDir = options.sessionDir ?? defaultSessionDirPath(cwd, agentDir);
  const sessionId = String(options.sessionId ?? options.resumeSessionId ?? "").trim();
  if (sessionId) {
    const directPath = resolve(sessionId);
    if (existsSync(directPath)) {
      return {
        sessionManager: SessionManager.open(directPath),
        sessionDir,
        resumed: true,
      };
    }
    if (existsSync(sessionDir)) {
      const sessions = await SessionManager.list(cwd, sessionDir);
      const found = sessions.find((entry) => entry.id === sessionId || entry.path === sessionId);
      if (found?.path) {
        return {
          sessionManager: SessionManager.open(found.path),
          sessionDir,
          resumed: true,
        };
      }
    }
  }
  return {
    sessionManager: SessionManager.create(cwd, sessionDir),
    sessionDir,
    resumed: false,
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
