import React, { useMemo, useState } from "react";
import { createRoot } from "react-dom/client";
import "../index.css";
import {
  ExtensionUIDialog,
  extensionUIPayloadLines,
  extensionUIPayloadString,
  extensionUIPayloadStringArray,
  isExtensionUIDialogMethod,
} from "../components/ExtensionUIDialog";
import type {
  ExtensionUIRequest,
  ExtensionUIResponse,
} from "../services/session";

type CapturedExtensionUIResponse = {
  type: "session.extension_ui_response";
  payload: {
    root_id: string;
    session_key: string;
    agent: string;
    request_id: string;
    method: string;
    value?: string;
    confirmed?: boolean;
    cancelled: boolean;
  };
};

type ExtensionUIChromeSnapshot = {
  statuses: Record<string, string>;
  widgets: Record<string, { lines: string[]; placement?: string }>;
  title: string;
};

type ExtensionUITestSnapshot = {
  responses: CapturedExtensionUIResponse[];
  chrome: ExtensionUIChromeSnapshot;
  editorText: string;
  notifications: string[];
};

declare global {
  interface Window {
    __extensionUITest: ExtensionUITestSnapshot;
  }
}

const rootId = "harness-root";
const sessionKey = "harness-session";
const agent = "pi";

const dialogRequests: Record<string, ExtensionUIRequest> = {
  select: {
    id: "select-1",
    method: "select",
    payload: {
      title: "Choose bridge route",
      options: ["rpc-first", "sdk-bridge"],
    },
  },
  confirm: {
    id: "confirm-1",
    method: "confirm",
    payload: {
      title: "Confirm SDK bridge",
      message: "Continue deterministic smoke?",
    },
  },
  input: {
    id: "input-1",
    method: "input",
    payload: {
      title: "Bridge input",
      placeholder: "type here",
    },
  },
  editor: {
    id: "editor-1",
    method: "editor",
    payload: {
      title: "Bridge editor",
      prefill: "initial text",
    },
  },
};

const fireAndForgetRequests: Record<string, ExtensionUIRequest> = {
  notify: {
    id: "notify-1",
    method: "notify",
    payload: {
      message: "ui-demo notification",
      notificationType: "info",
    },
  },
  setStatus: {
    id: "status-1",
    method: "setStatus",
    payload: {
      statusKey: "mindfs.pi_sdk_bridge",
      statusText: "running",
    },
  },
  setWidget: {
    id: "widget-1",
    method: "setWidget",
    payload: {
      widgetKey: "mindfs.pi_sdk_bridge",
      content: ["SDK bridge widget"],
      placement: "aboveEditor",
    },
  },
  setTitle: {
    id: "title-1",
    method: "setTitle",
    payload: {
      title: "MindFS Pi SDK Bridge Smoke",
    },
  },
  set_editor_text: {
    id: "editor-text-1",
    method: "set_editor_text",
    payload: {
      text: "prefilled by snake alias",
    },
  },
  setEditorText: {
    id: "editor-text-2",
    method: "setEditorText",
    payload: {
      text: "prefilled by camel alias",
    },
  },
};

function emptyChrome(): ExtensionUIChromeSnapshot {
  return {
    statuses: {},
    widgets: {},
    title: "",
  };
}

function emptySnapshot(): ExtensionUITestSnapshot {
  return {
    responses: [],
    chrome: emptyChrome(),
    editorText: "",
    notifications: [],
  };
}

function publishSnapshot(next: ExtensionUITestSnapshot) {
  window.__extensionUITest = next;
}

publishSnapshot(emptySnapshot());

function ExtensionUIHarness() {
  const [pendingRequest, setPendingRequest] = useState<ExtensionUIRequest | null>(null);
  const [inputValue, setInputValue] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [chrome, setChrome] = useState<ExtensionUIChromeSnapshot>(() => emptyChrome());
  const [editorText, setEditorText] = useState("");
  const [notifications, setNotifications] = useState<string[]>([]);
  const allRequests = useMemo(
    () => ({
      ...dialogRequests,
      ...fireAndForgetRequests,
    }),
    [],
  );

  function updatePublishedSnapshot(
    nextChrome = chrome,
    nextEditorText = editorText,
    nextNotifications = notifications,
  ) {
    publishSnapshot({
      ...window.__extensionUITest,
      chrome: nextChrome,
      editorText: nextEditorText,
      notifications: nextNotifications,
    });
  }

  function resetHarness() {
    const nextChrome = emptyChrome();
    setPendingRequest(null);
    setInputValue("");
    setSubmitting(false);
    setChrome(nextChrome);
    setEditorText("");
    setNotifications([]);
    document.title = "MindFS Extension UI Harness";
    publishSnapshot(emptySnapshot());
  }

  function openRequest(requestKey: string) {
    const request = allRequests[requestKey];
    if (!request) return;

    const method = `${request.method || ""}`;
    const payload = request.payload || {};
    if (isExtensionUIDialogMethod(method)) {
      setPendingRequest({ ...request, method, payload });
      setInputValue(
        extensionUIPayloadString(payload, "prefill") ||
          extensionUIPayloadString(payload, "value") ||
          "",
      );
      setSubmitting(false);
      return;
    }

    setPendingRequest(null);
    if (method === "notify") {
      const message =
        extensionUIPayloadString(payload, "message") ||
        extensionUIPayloadString(payload, "title") ||
        "Pi extension notification";
      const nextNotifications = [...notifications, message];
      setNotifications(nextNotifications);
      updatePublishedSnapshot(chrome, editorText, nextNotifications);
    } else if (method === "setStatus") {
      const statusKey = extensionUIPayloadString(payload, "statusKey") || request.id;
      const statusText = extensionUIPayloadString(payload, "statusText");
      const statuses = { ...chrome.statuses };
      if (statusText) statuses[statusKey] = statusText;
      else delete statuses[statusKey];
      const nextChrome = { ...chrome, statuses };
      setChrome(nextChrome);
      updatePublishedSnapshot(nextChrome);
    } else if (method === "setWidget") {
      const widgetKey = extensionUIPayloadString(payload, "widgetKey") || request.id;
      const legacyLines = extensionUIPayloadStringArray(payload, "widgetLines");
      const lines = legacyLines.length > 0 ? legacyLines : extensionUIPayloadLines(payload, "content");
      const placement =
        extensionUIPayloadString(payload, "widgetPlacement") ||
        extensionUIPayloadString(payload, "placement");
      const widgets = { ...chrome.widgets };
      if (lines.length > 0) widgets[widgetKey] = { lines, placement };
      else delete widgets[widgetKey];
      const nextChrome = { ...chrome, widgets };
      setChrome(nextChrome);
      updatePublishedSnapshot(nextChrome);
    } else if (method === "setTitle") {
      const title = extensionUIPayloadString(payload, "title");
      if (!title) return;
      document.title = title;
      const nextChrome = { ...chrome, title };
      setChrome(nextChrome);
      updatePublishedSnapshot(nextChrome);
    } else if (method === "set_editor_text" || method === "setEditorText") {
      const text = extensionUIPayloadString(payload, "text");
      setEditorText(text);
      updatePublishedSnapshot(chrome, text);
    }
  }

  function captureResponse(request: ExtensionUIRequest, response: ExtensionUIResponse) {
    const payload: CapturedExtensionUIResponse["payload"] = {
      root_id: rootId,
      session_key: sessionKey,
      agent,
      request_id: request.id,
      method: request.method,
      cancelled: response.cancelled === true,
    };
    if (response.value !== undefined) payload.value = response.value;
    if (response.confirmed !== undefined) payload.confirmed = response.confirmed;

    const nextResponses = [
      ...window.__extensionUITest.responses,
      {
        type: "session.extension_ui_response" as const,
        payload,
      },
    ];
    publishSnapshot({
      ...window.__extensionUITest,
      responses: nextResponses,
    });
  }

  function submitResponse(response: ExtensionUIResponse) {
    if (!pendingRequest) return;
    setSubmitting(true);
    captureResponse(pendingRequest, response);
    setSubmitting(false);
    setPendingRequest(null);
  }

  function cancelResponse() {
    submitResponse({ cancelled: true });
  }

  return (
    <main
      style={{
        minHeight: "100vh",
        background: "var(--bg-gradient-start)",
        color: "var(--text-primary)",
        padding: "24px",
        fontFamily: "Inter, ui-sans-serif, system-ui, sans-serif",
      }}
    >
      <section style={{ maxWidth: "760px", display: "flex", flexDirection: "column", gap: "14px" }}>
        <h1 style={{ margin: 0, fontSize: "20px" }}>Extension UI harness</h1>
        <div style={{ display: "flex", flexWrap: "wrap", gap: "8px" }}>
          {Object.keys(dialogRequests).map((requestKey) => (
            <button
              key={requestKey}
              data-testid={`request-${requestKey}`}
              type="button"
              onClick={() => openRequest(requestKey)}
            >
              {requestKey}
            </button>
          ))}
        </div>
        <div style={{ display: "flex", flexWrap: "wrap", gap: "8px" }}>
          {Object.keys(fireAndForgetRequests).map((requestKey) => (
            <button
              key={requestKey}
              data-testid={`request-${requestKey}`}
              type="button"
              onClick={() => openRequest(requestKey)}
            >
              {requestKey}
            </button>
          ))}
        </div>
        <button data-testid="request-reset" type="button" onClick={resetHarness}>
          reset
        </button>
        <pre
          data-testid="harness-state"
          style={{
            margin: 0,
            border: "1px solid var(--border-color)",
            borderRadius: "8px",
            padding: "12px",
            whiteSpace: "pre-wrap",
          }}
        >
          {JSON.stringify(window.__extensionUITest, null, 2)}
        </pre>
      </section>
      {pendingRequest ? (
        <ExtensionUIDialog
          request={pendingRequest}
          inputValue={inputValue}
          submitting={submitting}
          onInputValueChange={setInputValue}
          onSubmit={submitResponse}
          onCancel={cancelResponse}
        />
      ) : null}
    </main>
  );
}

const rootElement = document.getElementById("root");
if (!rootElement) {
  throw new Error("Missing #root element for extension UI harness");
}

createRoot(rootElement).render(
  <React.StrictMode>
    <ExtensionUIHarness />
  </React.StrictMode>,
);
