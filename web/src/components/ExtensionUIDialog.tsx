import type {
  ExtensionUIRequest,
  ExtensionUIResponse,
} from "../services/session";

export type ExtensionUIDialogRequest = Pick<
  ExtensionUIRequest,
  "id" | "method" | "payload"
>;

type ExtensionUIDialogProps = {
  request: ExtensionUIDialogRequest;
  inputValue: string;
  submitting: boolean;
  onInputValueChange: (value: string) => void;
  onSubmit: (response: ExtensionUIResponse) => void | Promise<void>;
  onCancel: () => void;
};

type ExtensionUISelectOption = {
  label: string;
  value: string;
  description?: string;
  disabled?: boolean;
};

export function extensionUIPayloadString(
  payload: Record<string, unknown> | undefined,
  key: string,
): string {
  const value = payload?.[key];
  return typeof value === "string" ? value : "";
}

export function extensionUIPayloadStringArray(
  payload: Record<string, unknown> | undefined,
  key: string,
): string[] {
  const value = payload?.[key];
  if (!Array.isArray(value)) return [];
  return value.filter((item): item is string => typeof item === "string");
}

function objectStringValue(value: Record<string, unknown>, keys: string[]): string {
  for (const key of keys) {
    const item = value[key];
    if (typeof item === "string" && item.trim() !== "") {
      return item;
    }
    if (typeof item === "number" || typeof item === "boolean") {
      return String(item);
    }
  }
  return "";
}

export function extensionUIPayloadSelectOptions(
  payload: Record<string, unknown> | undefined,
  key: string,
): ExtensionUISelectOption[] {
  const value = payload?.[key];
  if (!Array.isArray(value)) return [];
  const options: ExtensionUISelectOption[] = [];
  for (const item of value) {
    if (typeof item === "string") {
      options.push({ label: item, value: item });
      continue;
    }
    if (typeof item === "number" || typeof item === "boolean") {
      const text = String(item);
      options.push({ label: text, value: text });
      continue;
    }
    if (!item || typeof item !== "object" || Array.isArray(item)) {
      continue;
    }
    const raw = item as Record<string, unknown>;
    const label =
      objectStringValue(raw, ["label", "name", "title", "text", "id", "key", "value"]) ||
      "";
    const optionValue =
      objectStringValue(raw, ["value", "id", "key", "name", "label", "title", "text"]) ||
      label;
    if (!label || !optionValue) {
      continue;
    }
    const description = objectStringValue(raw, ["description", "detail", "subtitle"]);
    options.push({
      label,
      value: optionValue,
      description: description || undefined,
      disabled: raw.disabled === true,
    });
  }
  return options;
}

export function extensionUIPayloadLines(
  payload: Record<string, unknown> | undefined,
  key: string,
): string[] {
  const values = extensionUIPayloadStringArray(payload, key);
  if (values.length > 0) return values;
  const text = extensionUIPayloadString(payload, key);
  if (!text) return [];
  return text.split(/\r?\n/).filter((line) => line.trim() !== "");
}

export function isExtensionUIDialogMethod(method: string): boolean {
  return ["select", "confirm", "input", "editor"].includes(method);
}

export function extensionUITitle(request: ExtensionUIDialogRequest): string {
  const payload = request.payload || {};
  return (
    extensionUIPayloadString(payload, "title") ||
    extensionUIPayloadString(payload, "message") ||
    `Pi extension UI: ${request.method}`
  );
}

export function ExtensionUIDialog({
  request,
  inputValue,
  submitting,
  onInputValueChange,
  onSubmit,
  onCancel,
}: ExtensionUIDialogProps) {
  const title = extensionUITitle(request);
  const message = extensionUIPayloadString(request.payload, "message");
  const options = extensionUIPayloadSelectOptions(request.payload, "options");
  const placeholder = extensionUIPayloadString(request.payload, "placeholder");
  const disabledCursor = submitting ? "not-allowed" : "pointer";

  return (
    <div
      data-testid="extension-ui-dialog"
      role="dialog"
      aria-modal="true"
      aria-labelledby="extension-ui-dialog-title"
      style={{
        position: "fixed",
        inset: 0,
        background: "rgba(15, 23, 42, 0.42)",
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        padding: "24px",
        zIndex: 1800,
      }}
    >
      <div
        style={{
          width: "min(520px, 100%)",
          background: "#fff",
          borderRadius: "18px",
          padding: "20px",
          boxShadow: "0 28px 80px rgba(15, 23, 42, 0.22)",
          display: "flex",
          flexDirection: "column",
          gap: "14px",
        }}
      >
        <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
          <div
            data-testid="extension-ui-method"
            style={{ fontSize: "12px", color: "#64748b", fontWeight: 700 }}
          >
            Pi extension UI · {request.method}
          </div>
          <div
            id="extension-ui-dialog-title"
            data-testid="extension-ui-title"
            style={{ fontSize: "18px", fontWeight: 700, color: "#0f172a" }}
          >
            {title}
          </div>
          {message && message !== title ? (
            <div
              data-testid="extension-ui-message"
              style={{ color: "#475569", fontSize: "13px", lineHeight: 1.5 }}
            >
              {message}
            </div>
          ) : null}
        </div>
        {request.method === "select" ? (
          <div style={{ display: "flex", flexDirection: "column", gap: "8px" }}>
            {options.map((option, index) => (
              <button
                key={`${option.value}-${index}`}
                data-testid="extension-ui-option"
                type="button"
                disabled={submitting || option.disabled}
                onClick={() => void onSubmit({ value: option.value })}
                style={{
                  border: "1px solid rgba(148, 163, 184, 0.5)",
                  background: "#f8fafc",
                  borderRadius: "10px",
                  padding: "10px 12px",
                  textAlign: "left",
                  cursor: submitting || option.disabled ? "not-allowed" : disabledCursor,
                }}
              >
                <span style={{ display: "block" }}>{option.label}</span>
                {option.description ? (
                  <span
                    style={{
                      display: "block",
                      color: "#64748b",
                      fontSize: "12px",
                      lineHeight: 1.4,
                      marginTop: 3,
                    }}
                  >
                    {option.description}
                  </span>
                ) : null}
              </button>
            ))}
          </div>
        ) : request.method === "confirm" ? (
          <div style={{ display: "flex", justifyContent: "flex-end", gap: "10px" }}>
            <button
              data-testid="extension-ui-cancel"
              type="button"
              disabled={submitting}
              onClick={onCancel}
              style={{
                border: "1px solid #cbd5e1",
                background: "#fff",
                borderRadius: "999px",
                padding: "8px 14px",
                cursor: disabledCursor,
              }}
            >
              取消
            </button>
            <button
              data-testid="extension-ui-confirm-no"
              type="button"
              disabled={submitting}
              onClick={() => void onSubmit({ confirmed: false })}
              style={{
                border: "1px solid #cbd5e1",
                background: "#fff",
                borderRadius: "999px",
                padding: "8px 14px",
                cursor: disabledCursor,
              }}
            >
              否
            </button>
            <button
              data-testid="extension-ui-confirm-yes"
              type="button"
              disabled={submitting}
              onClick={() => void onSubmit({ confirmed: true })}
              style={{
                border: "none",
                background: "#0f172a",
                color: "#fff",
                borderRadius: "999px",
                padding: "8px 16px",
                cursor: disabledCursor,
              }}
            >
              是
            </button>
          </div>
        ) : request.method === "editor" ? (
          <textarea
            data-testid="extension-ui-editor"
            value={inputValue}
            onChange={(event) => onInputValueChange(event.target.value)}
            placeholder={placeholder}
            rows={8}
            style={{
              width: "100%",
              borderRadius: "12px",
              border: "1px solid rgba(148, 163, 184, 0.5)",
              padding: "12px",
              resize: "vertical",
              fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
              fontSize: "13px",
            }}
          />
        ) : (
          <input
            data-testid="extension-ui-input"
            type="text"
            value={inputValue}
            onChange={(event) => onInputValueChange(event.target.value)}
            placeholder={placeholder}
            autoFocus
            style={{
              width: "100%",
              borderRadius: "12px",
              border: "1px solid rgba(148, 163, 184, 0.5)",
              padding: "12px",
              fontSize: "14px",
            }}
          />
        )}
        {request.method === "input" || request.method === "editor" ? (
          <div style={{ display: "flex", justifyContent: "flex-end", gap: "10px" }}>
            <button
              data-testid="extension-ui-cancel"
              type="button"
              disabled={submitting}
              onClick={onCancel}
              style={{
                border: "1px solid #cbd5e1",
                background: "#fff",
                borderRadius: "999px",
                padding: "8px 14px",
                cursor: disabledCursor,
              }}
            >
              取消
            </button>
            <button
              data-testid="extension-ui-submit"
              type="button"
              disabled={submitting}
              onClick={() => void onSubmit({ value: inputValue })}
              style={{
                border: "none",
                background: "#0f172a",
                color: "#fff",
                borderRadius: "999px",
                padding: "8px 16px",
                cursor: disabledCursor,
              }}
            >
              提交
            </button>
          </div>
        ) : request.method === "select" ? (
          <div style={{ display: "flex", justifyContent: "flex-end" }}>
            <button
              data-testid="extension-ui-cancel"
              type="button"
              disabled={submitting}
              onClick={onCancel}
              style={{
                border: "1px solid #cbd5e1",
                background: "#fff",
                borderRadius: "999px",
                padding: "8px 14px",
                cursor: disabledCursor,
              }}
            >
              取消
            </button>
          </div>
        ) : null}
      </div>
    </div>
  );
}
