export type AppearanceMode = "dark" | "light" | "system";

export const APPEARANCE_STORAGE_KEY = "mindfs-appearance-mode";
export const APPEARANCE_CHANGE_EVENT = "mindfs:appearance-changed";

const appearanceModes = new Set<AppearanceMode>(["dark", "light", "system"]);

export function normalizeAppearanceMode(value: unknown): AppearanceMode {
  return appearanceModes.has(value as AppearanceMode) ? (value as AppearanceMode) : "system";
}

export function getAppearanceMode(): AppearanceMode {
  if (typeof window === "undefined") {
    return "system";
  }
  try {
    return normalizeAppearanceMode(window.localStorage.getItem(APPEARANCE_STORAGE_KEY));
  } catch {
    return "system";
  }
}

export function getEffectiveAppearanceMode(mode: AppearanceMode = getAppearanceMode()): "dark" | "light" {
  if (mode === "dark" || mode === "light") {
    return mode;
  }
  if (typeof window === "undefined") {
    return "light";
  }
  return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

export function applyAppearanceMode(mode: AppearanceMode = getAppearanceMode()): void {
  if (typeof document === "undefined") {
    return;
  }
  if (mode === "system") {
    document.documentElement.removeAttribute("data-theme");
    return;
  }
  document.documentElement.setAttribute("data-theme", mode);
}

export function setAppearanceMode(mode: AppearanceMode): void {
  if (typeof window === "undefined") {
    return;
  }
  const nextMode = normalizeAppearanceMode(mode);
  try {
    if (nextMode === "system") {
      window.localStorage.removeItem(APPEARANCE_STORAGE_KEY);
    } else {
      window.localStorage.setItem(APPEARANCE_STORAGE_KEY, nextMode);
    }
  } catch {
    // Keep the current page responsive even when storage is unavailable.
  }
  applyAppearanceMode(nextMode);
  window.dispatchEvent(new CustomEvent<AppearanceMode>(APPEARANCE_CHANGE_EVENT, { detail: nextMode }));
}
