import React, { useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import "../index.css";
import { AgentSelector } from "../components/AgentSelector";
import type { AgentStatus } from "../services/agents";

type AgentSelectorSnapshot = {
  agent: string;
  model: string;
  mode: string;
  refreshes: string[];
};

declare global {
  interface Window {
    __agentSelectorTest: AgentSelectorSnapshot;
    __agentSelectorResolvePiRefresh?: () => void;
  }
}

const healthyPi: AgentStatus = {
  name: "pi",
  installed: true,
  available: true,
  current_model_id: "cch-responses/gpt-5.5",
  default_model_id: "cch-responses/gpt-5.5",
  models: [
    {
      id: "cch-responses/gpt-5.5",
      name: "cch-responses/GPT 5.5",
    },
    {
      id: "cch-responses/gpt-5.4-mini",
      name: "cch-responses/GPT 5.4 Mini",
    },
  ],
  current_mode_id: "xhigh",
  modes: [
    {
      id: "xhigh",
      name: "Thinking: xhigh",
    },
  ],
};

const baseAgents: AgentStatus[] = [
  {
    name: "opencode",
    installed: true,
    available: true,
    models: [],
  },
  healthyPi,
];

function publishSnapshot(next: AgentSelectorSnapshot) {
  window.__agentSelectorTest = next;
}

function getInitialAgents(): AgentStatus[] {
  const scenario = new URLSearchParams(window.location.search).get("scenario");
  if (scenario !== "stale-pi") {
    return baseAgents;
  }
  return baseAgents.map((entry) =>
    entry.name === "pi"
      ? {
          name: "pi",
          installed: true,
          available: false,
          error: "Pi probe is still warming up",
          models: [],
          modes: [],
        }
      : entry,
  );
}

function AgentSelectorHarness() {
  const scenario = new URLSearchParams(window.location.search).get("scenario");
  const [agents, setAgents] = useState<AgentStatus[]>(() => getInitialAgents());
  const [agent, setAgent] = useState("opencode");
  const [model, setModel] = useState("");
  const [mode, setMode] = useState("");
  const [refreshes, setRefreshes] = useState<string[]>([]);

  useEffect(() => {
    publishSnapshot({ agent, model, mode, refreshes });
  }, [agent, model, mode, refreshes]);

  useEffect(() => {
    if (scenario !== "stale-pi") {
      delete window.__agentSelectorResolvePiRefresh;
      return;
    }
    window.__agentSelectorResolvePiRefresh = () => {
      setAgents((current) =>
        current.map((entry) => (entry.name === "pi" ? healthyPi : entry)),
      );
    };
    return () => {
      delete window.__agentSelectorResolvePiRefresh;
    };
  }, [scenario]);

  return (
    <main
      style={{
        minHeight: "100vh",
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        background: "var(--content-bg)",
        color: "var(--text-primary)",
      }}
    >
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: "12px",
          padding: "24px",
        }}
      >
        <div
          data-testid="selected-state"
          style={{
            minWidth: "220px",
            fontSize: "13px",
            color: "var(--text-secondary)",
          }}
        >
          {agent}:{model || "(none)"}:{mode || "(none)"}
        </div>
        <AgentSelector
          agent={agent}
          model={model}
          mode={mode}
          agents={agents}
          onAgentChange={(nextAgent, nextModel) => {
            setAgent(nextAgent);
            setModel(nextModel || "");
          }}
          onModeChange={(nextMode) => setMode(nextMode || "")}
          onAgentRefresh={(name) => {
            setRefreshes((current) => [...current, name]);
          }}
          compact
        />
      </div>
    </main>
  );
}

publishSnapshot({ agent: "opencode", model: "", mode: "", refreshes: [] });

const root = document.getElementById("root");
if (!root) {
  throw new Error("Missing #root element for agent selector harness");
}

createRoot(root).render(<AgentSelectorHarness />);
