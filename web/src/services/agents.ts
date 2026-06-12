import { appPath } from "./base";
import { protectedAPIReady, protectedJSON } from "./api";

// Agent status service

export type AgentStatus = {
  name: string;
  installed: boolean;
  available: boolean;
  version?: string;
  error?: string;
  last_probe?: string;
  current_model_id?: string;
  current_mode_id?: string;
  default_model_id?: string;
  default_effort?: string;
  default_fast_service?: string;
  supports_fast_service?: boolean;
  efforts?: string[];
  models?: AgentModelInfo[];
  modes?: AgentModeInfo[];
  models_error?: string;
  modes_error?: string;
  commands?: AgentCommandInfo[];
  commands_error?: string;
};

export type AgentModelInfo = {
  id: string;
  name: string;
  description?: string;
  hidden?: boolean;
  supportEffort?: boolean;
};

export type AgentModeInfo = {
  id: string;
  name: string;
  description?: string;
};

export type AgentCommandInfo = {
  name: string;
  description?: string;
  argument_hint?: string;
};

export type ShellStatus = {
  id: string;
  name?: string;
  label: string;
  command: string;
  resolved_command?: string;
  args?: string[];
  default?: boolean;
};

const VALID_EFFORTS = ["low", "medium", "high", "xhigh", "max"] as const;
function normalizeEfforts(input: unknown): string[] | undefined {
  if (!Array.isArray(input)) {
    return undefined;
  }
  const seen = new Set<string>();
  const efforts: string[] = [];
  for (const item of input) {
    const value = String(item || "").trim().toLowerCase();
    if (!VALID_EFFORTS.includes(value as (typeof VALID_EFFORTS)[number])) {
      continue;
    }
    if (seen.has(value)) {
      continue;
    }
    seen.add(value);
    efforts.push(value);
  }
  return efforts;
}

function normalizeAgentStatus(input: unknown): AgentStatus | null {
  if (!input || typeof input !== "object") {
    return null;
  }
  const agent = input as AgentStatus;
  return {
    ...agent,
    efforts: normalizeEfforts(agent.efforts),
    default_fast_service:
      typeof agent.default_fast_service === "string"
        ? agent.default_fast_service
        : "",
    supports_fast_service: !!agent.supports_fast_service,
  };
}

let cachedAgents: AgentStatus[] = [];
let cachedShells: ShellStatus[] = [];
let lastFetch = 0;
let inFlightAgents: Promise<{ agents: AgentStatus[]; shells: ShellStatus[] }> | null = null;
const CACHE_TTL = 30000; // 30 seconds

type FetchAgentsOptions = {
  force?: boolean;
  refreshAgent?: string;
};

function normalizeFetchOptions(options: boolean | FetchAgentsOptions = false): Required<FetchAgentsOptions> {
  if (typeof options === "boolean") {
    return { force: options, refreshAgent: "" };
  }
  return {
    force: !!options.force,
    refreshAgent: String(options.refreshAgent || "").trim(),
  };
}

function normalizeShellStatus(input: unknown): ShellStatus | null {
  if (!input || typeof input !== "object") {
    return null;
  }
  const shell = input as ShellStatus;
  const id = String(shell.id || shell.command || "").trim();
  const command = String(shell.command || id).trim();
  if (!id || !command) {
    return null;
  }
  return {
    id,
    command,
    name: typeof shell.name === "string" ? shell.name : undefined,
    resolved_command: typeof shell.resolved_command === "string" ? shell.resolved_command : undefined,
    label: String(shell.name || shell.label || id).trim() || id,
    args: Array.isArray(shell.args) ? shell.args.map((item) => String(item)) : undefined,
    default: !!shell.default,
  };
}

async function fetchAgentRuntime(options: boolean | FetchAgentsOptions = false): Promise<{ agents: AgentStatus[]; shells: ShellStatus[] }> {
  const { force, refreshAgent } = normalizeFetchOptions(options);
  const now = Date.now();
  if (!force && !refreshAgent && cachedAgents.length > 0 && now - lastFetch < CACHE_TTL) {
    return { agents: cachedAgents, shells: cachedShells };
  }
  if (!refreshAgent && inFlightAgents) {
    return inFlightAgents;
  }
  if (!protectedAPIReady()) {
    return { agents: cachedAgents, shells: cachedShells };
  }

  const request = async () => {
    const path = refreshAgent
      ? `/api/agents?refresh_agent=${encodeURIComponent(refreshAgent)}`
      : "/api/agents";
    const data = await protectedJSON<any>(appPath(path));
    const agentItems: unknown[] = Array.isArray(data)
      ? data
      : Array.isArray(data?.agents)
        ? data.agents
        : [];
    const shellItems: unknown[] = Array.isArray(data?.shells) ? data.shells : [];
    cachedAgents = agentItems
      ? agentItems.map(normalizeAgentStatus).filter((item): item is AgentStatus => item !== null)
      : [];
    cachedShells = shellItems.map(normalizeShellStatus).filter((item): item is ShellStatus => item !== null);
    lastFetch = now;
    return { agents: cachedAgents, shells: cachedShells };
  };

  const pending = request();
  if (refreshAgent) {
    try {
      return await pending;
    } catch (err) {
      console.error("Failed to refresh agent:", err);
      return { agents: cachedAgents, shells: cachedShells };
    }
  }

  inFlightAgents = pending;
  try {
    return await inFlightAgents;
  } catch (err) {
    console.error("Failed to fetch agents:", err);
    return { agents: cachedAgents, shells: cachedShells };
  } finally {
    inFlightAgents = null;
  }
}

export async function fetchAgents(options: boolean | FetchAgentsOptions = false): Promise<AgentStatus[]> {
  const data = await fetchAgentRuntime(options);
  return data.agents;
}

export async function fetchShells(force = false): Promise<ShellStatus[]> {
  const data = await fetchAgentRuntime(force);
  return data.shells;
}
