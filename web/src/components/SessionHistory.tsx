import React from "react";
import { ModeIcon } from "./ModeIcon";
import { InlineTokenText } from "./InlineTokenText";

type SessionInfo = {
  key: string;
  name: string;
  type: "chat" | "plugin" | "command";
  task_id?: string;
  agent: string;
  model?: string;
  mode?: string;
  effort?: string;
  pending?: boolean;
};

type Exchange = {
  role: "user" | "agent";
  content: string;
  timestamp?: string;
};

type SessionHistoryProps = {
  session: SessionInfo | null;
  exchanges?: Exchange[];
  relatedFiles?: { path: string; name: string; head?: string; repo_path?: string; repo_name?: string; repo_kind?: string }[];
  onRestore?: () => void;
  onFileClick?: (file: { path: string; head?: string; repo_path?: string; repo_name?: string; repo_kind?: string }) => void;
  onClose?: () => void;
};

const typeLabels: Record<string, string> = {
  chat: "对话",
  plugin: "视图插件",
  command: "命令执行",
  task: "任务",
  skill: "对话",
};

export function SessionHistory({
  session,
  exchanges = [],
  relatedFiles = [],
  onRestore,
  onFileClick,
  onClose,
}: SessionHistoryProps) {
  if (!session) return null;
  const relatedFileGroups = (() => {
    const repoGroups = relatedFiles.reduce<
      Array<{
        key: string;
        repoPath: string;
        repoName: string;
        repoKind: string;
        headGroups: Array<{ key: string; head: string; files: typeof relatedFiles }>;
      }>
    >((groups, file) => {
    const head = file.head || "";
    const rawRepoPath = file.repo_path || "";
    const isCurrentRepoRecord = !rawRepoPath || file.repo_name === "当前项目";
    const repoPath = isCurrentRepoRecord ? "" : rawRepoPath;
    const rawRepoKind = file.repo_kind || "";
    const repoKind = isCurrentRepoRecord && rawRepoKind !== "plain" ? "" : rawRepoKind;
    const repoKey = `${repoKind}\0${repoPath}`;
    let repoGroup = groups.find((group) => group.key === repoKey);
    if (!repoGroup) {
      repoGroup = {
        key: repoKey,
        repoPath,
        repoName: isCurrentRepoRecord
          ? "当前项目"
          : file.repo_name || repoPath.split(/[\\/]/).filter(Boolean).pop() || "当前项目",
        repoKind,
        headGroups: [],
      };
      groups.push(repoGroup);
    }
    const headKey = `${repoKey}\0${head}`;
    const existing = repoGroup.headGroups.find((group) => group.key === headKey);
    if (existing) {
      existing.files.push(file);
    } else {
      repoGroup.headGroups.push({
        key: headKey,
        head,
        files: [file],
      });
    }
    return groups;
    }, []);
    return repoGroups.flatMap((repoGroup) =>
      repoGroup.headGroups.map((headGroup) => ({
        key: headGroup.key,
        head: headGroup.head,
        repoPath: repoGroup.repoPath,
        repoName: repoGroup.repoName,
        repoKind: repoGroup.repoKind,
        files: headGroup.files,
      })),
    );
  })();

  return (
    <div
      style={{
        flex: 1,
        minHeight: 0,
        display: "flex",
        flexDirection: "column",
        background: "#fff",
      }}
    >
      {/* Header */}
      <div
        style={{
          padding: "16px 20px",
          borderBottom: "1px solid var(--border-color)",
          display: "flex",
          alignItems: "center",
          gap: "12px",
          background: "rgba(0,0,0,0.02)",
        }}
      >
        <ModeIcon type={session.task_id ? "task" : session.type || "chat"} size={20} />
        <div style={{ flex: 1 }}>
          <div style={{ fontSize: "16px", fontWeight: 600 }}>
            {session.name || `Session ${session.key.slice(0, 8)}`}
          </div>
          <div style={{ fontSize: "12px", color: "var(--text-secondary)" }}>
            {typeLabels[session.task_id ? "task" : session.type]} · {session.agent || "-"} · 已关闭
          </div>
        </div>
        <button
          onClick={onRestore}
          style={{
            padding: "8px 16px",
            borderRadius: "8px",
            border: "1px solid #3b82f6",
            background: "#fff",
            color: "#3b82f6",
            fontSize: "13px",
            fontWeight: 500,
            cursor: "pointer",
            display: "flex",
            alignItems: "center",
            gap: "6px",
          }}
        >
          ↻ 恢复
        </button>
        {onClose && (
          <button
            onClick={onClose}
            style={{
              background: "none",
              border: "none",
              cursor: "pointer",
              fontSize: "18px",
              color: "var(--text-secondary)",
              padding: "4px 8px",
            }}
          >
            ✕
          </button>
        )}
      </div>

      {/* Content */}
      <div
        style={{
          flex: 1,
          minHeight: 0,
          overflow: "auto",
          padding: "20px",
        }}
      >
        {/* 对话历史 */}
        <div style={{ marginBottom: "24px" }}>
          <div
            style={{
              fontSize: "14px",
              fontWeight: 600,
              marginBottom: "16px",
              color: "var(--text-primary)",
            }}
          >
            对话历史
          </div>
          <div style={{ display: "flex", flexDirection: "column", gap: "16px" }}>
            {exchanges.map((ex, i) => (
              <div
                key={i}
                style={{
                  display: "flex",
                  flexDirection: "column",
                  gap: "4px",
                }}
              >
                <div
                  style={{
                    fontSize: "11px",
                    color: "var(--text-secondary)",
                    fontWeight: 500,
                  }}
                >
                  {ex.role === "user" ? "用户" : "Agent"}
                  {ex.timestamp && ` · ${ex.timestamp}`}
                </div>
                <div
                  style={{
                    padding: "12px 16px",
                    borderRadius: ex.role === "user" ? "12px 12px 12px 4px" : "12px 12px 4px 12px",
                    background: ex.role === "user" ? "rgba(148,163,184,0.14)" : "rgba(0,0,0,0.05)",
                    color: "var(--text-primary)",
                    fontSize: "13px",
                    lineHeight: 1.6,
                    whiteSpace: "pre-wrap",
                    maxWidth: "85%",
                    alignSelf: ex.role === "user" ? "flex-start" : "flex-start",
                  }}
                >
                  {ex.role === "user" ? (
                    <InlineTokenText content={ex.content} variant="inverse" />
                  ) : (
                    ex.content
                  )}
                </div>
              </div>
            ))}
          </div>
        </div>

        {/* 关联文件 */}
        {relatedFiles.length > 0 && (
          <div>
            <div
              style={{
                fontSize: "14px",
                fontWeight: 600,
                marginBottom: "12px",
                color: "var(--text-primary)",
              }}
            >
              关联文件
            </div>
            <div style={{ display: "flex", flexDirection: "column", gap: "8px" }}>
              {relatedFileGroups.map((group) => {
                const isCurrentRepo = !group.repoPath || group.repoName === "当前项目";
                const showGroupHeader = group.repoKind === "plain" || group.head || !isCurrentRepo;
                return (
                  <div key={group.key} style={{ display: "flex", flexDirection: "column", gap: "8px" }}>
                    {showGroupHeader ? (
                      <div
                        title={[group.repoPath, group.head].filter(Boolean).join(" · ") || group.repoName || "当前项目"}
                        style={{
                          fontSize: "11px",
                          color: "var(--text-secondary)",
                          fontFamily: group.head ? "var(--mono-font, monospace)" : undefined,
                        }}
                      >
                        {group.repoKind === "plain"
                          ? `${group.repoName || "当前项目"} · 非 Git`
                          : group.head
                            ? isCurrentRepo
                              ? `HEAD ${group.head.slice(0, 8)}`
                              : `${group.repoName || "当前项目"} · HEAD ${group.head.slice(0, 8)}`
                            : group.repoName || "当前项目"}
                      </div>
                    ) : null}
                    {group.files.map((file) => (
                <button
                  key={`${file.head || "legacy"}:${file.path}`}
                  onClick={() => onFileClick?.(file)}
                  style={{
                    display: "flex",
                    alignItems: "center",
                    gap: "8px",
                    padding: "10px 12px",
                    background: "rgba(0,0,0,0.02)",
                    border: "1px solid var(--border-color)",
                    borderRadius: "8px",
                    cursor: "pointer",
                    textAlign: "left",
                    transition: "background 0.15s",
                  }}
                  onMouseEnter={(e) => {
                    e.currentTarget.style.background = "rgba(0,0,0,0.05)";
                  }}
                  onMouseLeave={(e) => {
                    e.currentTarget.style.background = "rgba(0,0,0,0.02)";
                  }}
                >
                  <span style={{ fontSize: "14px" }}>📄</span>
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <div
                      style={{
                        fontSize: "13px",
                        fontWeight: 500,
                        overflow: "hidden",
                        textOverflow: "ellipsis",
                        whiteSpace: "nowrap",
                      }}
                    >
                      {file.name}
                    </div>
                    <div
                      style={{
                        fontSize: "11px",
                        color: "var(--text-secondary)",
                        overflow: "hidden",
                        textOverflow: "ellipsis",
                        whiteSpace: "nowrap",
                      }}
                    >
                      {file.path}
                    </div>
                  </div>
                  <span style={{ fontSize: "12px", color: "var(--text-secondary)" }}>→</span>
                </button>
                    ))}
                  </div>
                );
              })}
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
