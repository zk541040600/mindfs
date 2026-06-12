import React, { useState } from "react";
import { createRoot } from "react-dom/client";
import "../index.css";
import { SessionViewer } from "../components/SessionViewer";
import { sessionService } from "../services/session";

const sessionKey = "session-viewer-harness";

function SessionViewerHarness() {
  const [pending, setPending] = useState(true);
  const session = {
    key: sessionKey,
    session_key: sessionKey,
    name: "Session viewer harness",
    agent: "pi",
    pending,
    exchanges: [
      {
        role: "user",
        content: "hello",
        timestamp: "2026-06-12T00:00:00.000Z",
      },
      {
        role: "assistant",
        content: "already answered",
        timestamp: "2026-06-12T00:00:01.000Z",
      },
    ],
  };

  return (
    <main
      style={{
        height: "100vh",
        display: "flex",
        flexDirection: "column",
        background: "var(--content-bg)",
        color: "var(--text-primary)",
      }}
    >
      <div style={{ display: "flex", gap: "8px", padding: "12px" }}>
        <button
          data-testid="emit-stream"
          type="button"
          onClick={() =>
            sessionService.emitTestStreamEvent(sessionKey, {
              type: "message_chunk",
              data: { content: "streaming" },
            })
          }
        >
          emit stream
        </button>
        <button
          data-testid="clear-pending"
          type="button"
          onClick={() => setPending(false)}
        >
          clear pending
        </button>
      </div>
      <SessionViewer session={session} rootId="harness-root" />
    </main>
  );
}

const rootElement = document.getElementById("root");
if (!rootElement) {
  throw new Error("Missing #root element for session viewer harness");
}

createRoot(rootElement).render(<SessionViewerHarness />);
