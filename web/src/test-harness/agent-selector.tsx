import React, { useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import "../index.css";
import { AgentSelector } from "../components/AgentSelector";
import type { AgentStatus } from "../services/agents";

type AgentSelectorSnapshot = {
  agent: string;
  model: string;
  mode: string;
};

declare global {
  interface Window {
    __agentSelectorTest: AgentSelectorSnapshot;
  }
}

const agents: AgentStatus[] = [
  {
    name: "opencode",
    installed: true,
    available: true,
    models: [],
  },
  {
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
  },
];

function publishSnapshot(next: AgentSelectorSnapshot) {
  window.__agentSelectorTest = next;
}

function AgentSelectorHarness() {
  const [agent, setAgent] = useState("opencode");
  const [model, setModel] = useState("");
  const [mode, setMode] = useState("");

  useEffect(() => {
    publishSnapshot({ agent, model, mode });
  }, [agent, model, mode]);

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
          compact
        />
      </div>
    </main>
  );
}

publishSnapshot({ agent: "opencode", model: "", mode: "" });

const root = document.getElementById("root");
if (!root) {
  throw new Error("Missing #root element for agent selector harness");
}

createRoot(root).render(<AgentSelectorHarness />);
