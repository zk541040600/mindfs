import React from "react";
import type { SessionItem } from "./SessionList";

type SDKStatus = {
  enabled: boolean;
  agent: string;
  available: boolean;
  last_latency_ms?: number;
  last_error?: string;
  last_checked_at?: string;
  cache_entries?: number;
  ttl_ms?: number;
  capabilities?: string[];
};

type ExternalSessionListProps = {
  sessions: SessionItem[];
  selectedKey?: string;
  selectedAgent?: string;
  importingKey?: string;
  importingKeys?: Set<string>;
  selectedImportKeys?: Set<string>;
  filterBound?: boolean;
  headerAction?: React.ReactNode;
  sdkStatus?: SDKStatus | null;
  sdkStatusLoading?: boolean;
  onBack?: () => void;
  onSelect?: (session: SessionItem) => void;
  onToggleImport?: (session: SessionItem) => void;
  onConfirmImport?: () => void;
  onLoadOlder?: () => void;
  onRefresh?: () => void;
  loading?: boolean;
  loadingOlder?: boolean;
  sdkRefreshing?: boolean;
  confirmingImport?: boolean;
  hasMore?: boolean;
};

export function ExternalSessionList({
  sessions,
  selectedKey = "",
  selectedAgent = "",
  importingKey = "",
  importingKeys,
  selectedImportKeys,
  filterBound = true,
  headerAction,
  sdkStatus,
  sdkStatusLoading = false,
  onBack,
  onSelect,
  onToggleImport,
  onConfirmImport,
  onLoadOlder,
  onRefresh,
  loading = false,
  loadingOlder = false,
  sdkRefreshing = false,
  confirmingImport = false,
  hasMore = false,
}: ExternalSessionListProps) {
  const selectedCount = selectedImportKeys?.size || 0;
  const busy = confirmingImport || Boolean(importingKey) || Boolean(importingKeys?.size);
  return (
    <div
      style={{
        flex: 1,
        minHeight: 0,
        display: "flex",
        flexDirection: "column",
        background: "transparent",
      }}
    >
      <div
        style={{
          height: "36px",
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
          padding: "0 10px 0 4px",
          borderBottom: "1px solid var(--border-color)",
          background: "var(--mindfs-topbar-bg, transparent)",
          flexShrink: 0,
          boxSizing: "border-box",
        }}
      >
        <button
          type="button"
          onClick={onBack}
          aria-label="退出导入模式"
          style={iconButtonStyle(false)}
        >
          <ChevronLeftIcon />
        </button>
        {headerAction ? (
          <div style={{ display: "inline-flex", alignItems: "center" }}>
            {headerAction}
          </div>
        ) : null}
      </div>

      {selectedAgent === "pi" ? (
        <SDKStatusBar
          status={sdkStatus}
          loading={sdkStatusLoading}
          refreshing={sdkRefreshing}
          disabled={busy || loading}
          onRefresh={onRefresh}
        />
      ) : null}

      <div style={{ flex: 1, minHeight: 0, overflow: "auto", padding: "8px" }}>
        {loading ? (
          <div style={emptyStyle}>正在加载可导入会话...</div>
        ) : !selectedAgent ? (
          <div style={emptyStyle}>选择一个 Agent 查看可导入会话</div>
        ) : !sessions.length ? (
          <div style={emptyStyle}>
            {filterBound
              ? "没有找到可导入会话，或当前结果都已导入"
              : "没有找到可导入会话"}
          </div>
        ) : (
          <div style={{ display: "flex", flexDirection: "column", gap: "2px" }}>
            {sessions.map((session) => (
              <ExternalSessionCard
                key={session.key}
                session={session}
                selected={session.key === selectedKey}
                checked={Boolean(selectedImportKeys?.has(externalSessionKey(session)))}
                importing={
                  String(session.key || "") === importingKey ||
                  Boolean(importingKeys?.has(externalSessionKey(session)))
                }
                importDisabled={busy}
                onSelect={onSelect}
                onToggleImport={onToggleImport}
              />
            ))}
            {hasMore ? (
              <button
                type="button"
                onClick={onLoadOlder}
                disabled={loadingOlder}
                style={{
                  marginTop: "8px",
                  border: "1px solid var(--border-color)",
                  background: "transparent",
                  color: "var(--text-secondary)",
                  borderRadius: "8px",
                  padding: "8px 10px",
                  cursor: loadingOlder ? "default" : "pointer",
                  fontSize: "12px",
                }}
              >
                {loadingOlder ? "加载中..." : "加载更多"}
              </button>
            ) : null}
          </div>
        )}
      </div>
      {selectedAgent && sessions.length ? (
        <div
          style={{
            flexShrink: 0,
            borderTop: "1px solid var(--border-color)",
            padding: "8px 10px",
            background: "var(--mindfs-topbar-bg, transparent)",
          }}
        >
          <button
            type="button"
            disabled={!selectedCount || busy}
            onClick={onConfirmImport}
            style={{
              width: "100%",
              border: "1px solid var(--border-color)",
              borderRadius: "10px",
              background: "var(--accent-color)",
              color: "#fff",
              padding: "10px 12px",
              fontSize: "12px",
              fontWeight: 600,
              display: "flex",
              alignItems: "center",
              justifyContent: "center",
              gap: "8px",
              cursor: !selectedCount || busy ? "not-allowed" : "pointer",
              opacity: !selectedCount || busy ? 0.72 : 1,
              whiteSpace: "nowrap",
            }}
          >
            {busy
              ? "导入中..."
              : `${selectedAgent === "pi" ? "安全导入" : "确认导入"} ${selectedCount} 项`}
          </button>
        </div>
      ) : null}
    </div>
  );
}

function SDKStatusBar({
  status,
  loading,
  refreshing,
  disabled,
  onRefresh,
}: {
  status?: SDKStatus | null;
  loading: boolean;
  refreshing: boolean;
  disabled: boolean;
  onRefresh?: () => void;
}) {
  const enabled = Boolean(status?.enabled);
  const checked = status?.last_checked_at && !status.last_checked_at.startsWith("0001-");
  const label = loading
    ? "SDK 状态加载中"
    : !enabled
      ? "SDK 未启用"
      : status?.available
        ? "SDK 可用"
        : checked
          ? "SDK 不可用"
          : "SDK 未检查";
  const meta: string[] = [];
  if (typeof status?.last_latency_ms === "number" && status.last_latency_ms > 0) {
    meta.push(`${status.last_latency_ms}ms`);
  }
  if (typeof status?.cache_entries === "number") {
    meta.push(`缓存 ${status.cache_entries}`);
  }
  if (typeof status?.ttl_ms === "number" && status.ttl_ms > 0) {
    meta.push(`TTL ${Math.round(status.ttl_ms / 1000)}s`);
  }
  const dotColor = status?.available
    ? "#10b981"
    : checked
      ? "#f59e0b"
      : "var(--text-muted)";
  return (
    <div
      style={{
        flexShrink: 0,
        display: "flex",
        alignItems: "center",
        gap: "8px",
        padding: "7px 10px",
        borderBottom: "1px solid var(--border-color)",
        background: "var(--mindfs-topbar-bg, transparent)",
      }}
    >
      <span
        aria-hidden="true"
        style={{
          width: "7px",
          height: "7px",
          borderRadius: "50%",
          background: dotColor,
          flexShrink: 0,
        }}
      />
      <div style={{ minWidth: 0, flex: 1 }}>
        <div
          style={{
            color: "var(--text-primary)",
            fontSize: "11px",
            fontWeight: 700,
            whiteSpace: "nowrap",
            overflow: "hidden",
            textOverflow: "ellipsis",
          }}
          title={status?.last_error || label}
        >
          {label}
          {meta.length ? ` · ${meta.join(" · ")}` : ""}
        </div>
        {status?.last_error ? (
          <div
            style={{
              color: "var(--text-secondary)",
              fontSize: "10px",
              whiteSpace: "nowrap",
              overflow: "hidden",
              textOverflow: "ellipsis",
              marginTop: "2px",
            }}
            title={status.last_error}
          >
            {status.last_error}
          </div>
        ) : null}
      </div>
      {onRefresh ? (
        <button
          type="button"
          disabled={disabled || refreshing}
          onClick={onRefresh}
          style={{
            border: "1px solid var(--border-color)",
            background: "transparent",
            color: "var(--text-secondary)",
            borderRadius: "8px",
            padding: "5px 8px",
            fontSize: "11px",
            cursor: disabled || refreshing ? "not-allowed" : "pointer",
            opacity: disabled || refreshing ? 0.65 : 1,
            whiteSpace: "nowrap",
          }}
        >
          {refreshing ? "刷新中" : "刷新"}
        </button>
      ) : null}
    </div>
  );
}

function ExternalSessionCard({
  session,
  selected,
  checked,
  importing,
  importDisabled,
  onSelect,
  onToggleImport,
}: {
  session: SessionItem;
  selected: boolean;
  checked: boolean;
  importing: boolean;
  importDisabled: boolean;
  onSelect?: (session: SessionItem) => void;
  onToggleImport?: (session: SessionItem) => void;
}) {
  const displayName = session.name || session.key || "External Session";
  const subtitle = formatTime(session.updated_at || session.created_at || "");

  return (
    <div
      style={{
        width: "100%",
        display: "flex",
        alignItems: "center",
        gap: "2px",
        padding: "2px 0",
        borderRadius: "8px",
        position: "relative",
      }}
    >
      <button
        type="button"
        onClick={() => {
          if (onToggleImport && !importDisabled) {
            onToggleImport(session);
            return;
          }
          onSelect?.(session);
        }}
        style={{
          textAlign: "left",
          padding: "7px 6px 7px 6px",
          borderRadius: "8px",
          border: "1px solid transparent",
          background: checked
            ? "var(--selection-bg)"
            : selected
              ? "rgba(59, 130, 246, 0.1)"
              : "transparent",
          cursor: "pointer",
          flex: 1,
          minWidth: 0,
          display: "flex",
          alignItems: "center",
          gap: "8px",
          transition: "all 0.15s ease",
        }}
      >
        <div style={{ minWidth: 0, flex: 1 }}>
          <div
            style={{
              fontSize: "13px",
              fontWeight: selected ? 600 : 500,
              color:
                checked || selected
                  ? "var(--accent-color)"
                  : "var(--text-primary)",
              whiteSpace: "nowrap",
              overflow: "hidden",
              textOverflow: "ellipsis",
            }}
          >
            {displayName}
          </div>
          {subtitle ? (
            <div
              style={{
                fontSize: "10px",
                color: "var(--text-secondary)",
                marginTop: "2px",
                display: "inline-flex",
                alignItems: "center",
                gap: "6px",
                minWidth: 0,
              }}
            >
              <span
                style={{
                  whiteSpace: "nowrap",
                  overflow: "hidden",
                  textOverflow: "ellipsis",
                }}
              >
                {subtitle}
              </span>
              {importing ? <SpinnerIcon /> : null}
            </div>
          ) : null}
        </div>
      </button>

      {importing ? (
        <div style={{ flexShrink: 0, color: "var(--text-secondary)" }}>
          <SpinnerIcon />
        </div>
      ) : null}
    </div>
  );
}

function externalSessionKey(session: SessionItem): string {
  return String((session as any)?.agent_session_id || session.key || "").trim();
}

function formatTime(value?: string) {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return new Intl.DateTimeFormat("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  }).format(date);
}

const emptyStyle: React.CSSProperties = {
  fontSize: "12px",
  color: "var(--text-secondary)",
  padding: "12px 8px",
};

function iconButtonStyle(withGap: boolean): React.CSSProperties {
  return {
    border: "none",
    background: "transparent",
    color: "var(--text-secondary)",
    display: "inline-flex",
    alignItems: "center",
    justifyContent: "center",
    gap: withGap ? "2px" : 0,
    height: "28px",
    minWidth: "28px",
    borderRadius: "8px",
    cursor: "pointer",
    padding: withGap ? "0 6px" : 0,
  };
}

function ChevronLeftIcon() {
  return (
    <svg
      width="14"
      height="14"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d="m15 18-6-6 6-6" />
    </svg>
  );
}

function SpinnerIcon() {
  return (
    <svg width="10" height="10" viewBox="0 0 16 16" aria-hidden="true">
      <circle
        cx="8"
        cy="8"
        r="5.5"
        stroke="currentColor"
        strokeOpacity="0.22"
        strokeWidth="1.5"
        fill="none"
      />
      <path
        d="M8 2.5a5.5 5.5 0 0 1 5.5 5.5"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
        fill="none"
      >
        <animateTransform
          attributeName="transform"
          type="rotate"
          from="0 8 8"
          to="360 8 8"
          dur="0.8s"
          repeatCount="indefinite"
        />
      </path>
    </svg>
  );
}
