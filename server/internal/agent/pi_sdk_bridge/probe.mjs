#!/usr/bin/env node
/**
 * Experimental Pi SDK bridge probe for MindFS.
 *
 * This file is intentionally standalone and reversible. It exercises SDK-only
 * integration seams without changing MindFS' production pi-rpc path.
 */
import {
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
} from "/root/node-v22.22.0-linux-x64/lib/node_modules/@earendil-works/pi-coding-agent/dist/index.js";
import { createInterface } from "node:readline";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

const BRIDGE_PROTOCOL_VERSION = 1;
const DEFAULT_CWD = "/root/mindfs";
const DEFAULT_AGENT_DIR = "/root/.pi/agent";
const MAX_RESOURCE_ITEMS = 50;
const MAX_SESSION_ITEMS = 50;

for (const method of ["log", "info", "warn", "error", "debug"]) {
  console[method] = (...args) => process.stderr.write(`${args.map(String).join(" ")}\n`);
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
      "session-smoke --cwd /root/mindfs --json",
      "extension-ui-smoke --json",
      "runtime-replacement-smoke --cwd /root/mindfs --json",
      "jsonl",
    ],
    notes: [
      "Experimental probe only; does not modify MindFS agents.json or pi-rpc defaults.",
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
    name: info.name,
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
        if (request.scenario !== "extension-ui") {
          throw new ProbeError("E_PARAM", `unsupported test runtime scenario: ${request.scenario}`);
        }
        runtime = createJsonlUIRuntime();
        writeJsonl({ id: request.id, type: "response", command: "start_test_runtime", success: true, data: { scenario: request.scenario } });
      } else if (request.type === "prompt") {
        if (!runtime) {
          throw new ProbeError("E_STATE", "start_test_runtime must be sent before prompt");
        }
        if (request.message !== "/ui-demo") {
          throw new ProbeError("E_PARAM", "jsonl smoke currently supports only /ui-demo");
        }
        for (const event of buildJsonlUIEvents()) {
          runtime.pending.set(event.id, event);
          writeJsonl(event);
        }
        writeJsonl({ id: request.id, type: "response", command: "prompt", success: true, data: { queuedUIRequests: runtime.pending.size } });
      } else if (request.type === "extension_ui_response") {
        if (!runtime) {
          throw new ProbeError("E_STATE", "start_test_runtime must be sent before extension_ui_response");
        }
        const pending = runtime.pending.get(request.id);
        if (!pending) {
          throw new ProbeError("E_PARAM", `unknown extension UI request id: ${request.id}`);
        }
        runtime.pending.delete(request.id);
        runtime.responses.push({ id: request.id, method: pending.method, value: request.value, confirmed: request.confirmed, text: request.text });
        writeJsonl({ id: request.id, type: "response", command: "extension_ui_response", success: true, data: { method: pending.method, remaining: runtime.pending.size } });
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
}

function createJsonlUIRuntime() {
  return {
    pending: new Map(),
    responses: [],
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
