function canUseStorage(): boolean {
  return typeof window !== "undefined";
}

export function getStoredString(key: string): string | null {
  if (!canUseStorage()) {
    return null;
  }
  try {
    return window.localStorage.getItem(key);
  } catch {
    return null;
  }
}

export function setStoredString(key: string, value: string): void {
  if (!canUseStorage()) {
    return;
  }
  try {
    window.localStorage.setItem(key, value);
  } catch {}
}

export function removeStoredString(key: string): void {
  if (!canUseStorage()) {
    return;
  }
  try {
    window.localStorage.removeItem(key);
  } catch {}
}

const TOKEN_KEY = "mindfs_token";
const API_BASE_URL_KEY = "mindfs_api_base_url";
const WS_BASE_URL_KEY = "mindfs_ws_base_url";
const LAUNCHER_NODES_KEY = "mindfs_launcher_nodes";

export type LauncherNode = {
  id: string;
  name: string;
  url: string;
  createdAt: string;
  lastOpenedAt?: string;
};

export function getStoredToken(): string | null {
  return getStoredString(TOKEN_KEY);
}

export function setStoredToken(token: string): void {
  setStoredString(TOKEN_KEY, token);
}

export function clearStoredToken(): void {
  removeStoredString(TOKEN_KEY);
}

export function getStoredApiBaseURL(): string | null {
  return getStoredString(API_BASE_URL_KEY);
}

export function setStoredApiBaseURL(value: string): void {
  setStoredString(API_BASE_URL_KEY, value);
}

export function getStoredWsBaseURL(): string | null {
  return getStoredString(WS_BASE_URL_KEY);
}

export function setStoredWsBaseURL(value: string): void {
  setStoredString(WS_BASE_URL_KEY, value);
}

export function getStoredLauncherNodes(): LauncherNode[] {
  const raw = getStoredString(LAUNCHER_NODES_KEY);
  if (!raw) {
    return [];
  }
  try {
    const parsed = JSON.parse(raw);
    if (!Array.isArray(parsed)) {
      return [];
    }
    return parsed
      .map((item): LauncherNode | null => {
        if (!item || typeof item !== "object") {
          return null;
        }
        const raw = item as Record<string, unknown>;
        const id = typeof raw.id === "string" ? raw.id.trim() : "";
        const name = typeof raw.name === "string" ? raw.name.trim() : "";
        const url = typeof raw.url === "string" ? raw.url.trim() : "";
        const createdAt = typeof raw.createdAt === "string" ? raw.createdAt.trim() : "";
        const lastOpenedAt =
          typeof raw.lastOpenedAt === "string" ? raw.lastOpenedAt.trim() : undefined;
        if (!id || !name || !url || !createdAt) {
          return null;
        }
        const node: LauncherNode = { id, name, url, createdAt };
        if (lastOpenedAt) {
          node.lastOpenedAt = lastOpenedAt;
        }
        return node;
      })
      .filter((item): item is LauncherNode => item !== null);
  } catch {
    return [];
  }
}

export function setStoredLauncherNodes(nodes: LauncherNode[]): void {
  setStoredString(LAUNCHER_NODES_KEY, JSON.stringify(nodes));
}
