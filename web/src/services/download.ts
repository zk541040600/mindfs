import { registerPlugin } from "@capacitor/core";
import { Directory, Filesystem } from "@capacitor/filesystem";
import { appURL } from "./base";
import { e2eeService } from "./e2ee";
import { fetchProofProtectedBlob } from "./file";
import { getNativeBridge } from "./nativeBridge";
import { getApiBaseURL, isNativeShellRuntime } from "./runtime";

type DownloadFileParams = {
  rootId: string;
  path: string;
  name?: string;
};

type NativeDownloadPlugin = {
  download: (opts: { url: string; filename: string }) => Promise<{
    downloadId: number;
    filename: string;
    directory: string;
  }>;
  saveBase64: (opts: { data: string; filename: string }) => Promise<{
    filename: string;
    directory: string;
    uri?: string;
  }>;
};

const NativeDownload = registerPlugin<NativeDownloadPlugin>("NativeDownload");

type WindowWithNativeDownloadBridge = Window & {
  MindFSNativeDownload?: {
    download?: (url: string, filename: string) => string;
    downloadBase64?: (data: string, filename: string) => string;
  };
};

const invalidDownloadNamePattern = /[\x00-\x1f\x7f<>:"/\\|?*]+/g;
const maxDownloadNameLength = 180;

export function sanitizeDownloadName(path: string, name?: string): string {
  const candidate = String(name || path || "").trim();
  if (!candidate) {
    return "download";
  }
  const parts = candidate.replace(/\\/g, "/").split("/").filter(Boolean);
  const basename = parts[parts.length - 1] || "";
  const cleaned = basename
    .replace(invalidDownloadNamePattern, "_")
    .replace(/\s+/g, " ")
    .trim()
    .replace(/[. ]+$/g, "");
  if (!cleaned || cleaned === "." || cleaned === "..") {
    return "download";
  }
  return truncateDownloadName(cleaned);
}

function truncateDownloadName(name: string): string {
  if (name.length <= maxDownloadNameLength) {
    return name;
  }
  const dotIndex = name.lastIndexOf(".");
  const ext = dotIndex > 0 ? name.slice(dotIndex) : "";
  if (ext.length > 0 && ext.length <= 24) {
    return `${name.slice(0, maxDownloadNameLength - ext.length)}${ext}`;
  }
  return name.slice(0, maxDownloadNameLength);
}

function buildDownloadURL(rootId: string, path: string): string {
  return appURL("/api/file", new URLSearchParams({
    raw: "1",
    root: rootId,
    path,
    download: "1",
  }));
}

function toAbsoluteDownloadURL(url: string): string {
  if (/^https?:\/\//i.test(url)) {
    return url;
  }

  const apiBaseURL = getApiBaseURL();
  if (apiBaseURL) {
    return new URL(url, `${apiBaseURL.replace(/\/+$/, "")}/`).toString();
  }

  if (typeof window !== "undefined" && /^https?:$/i.test(window.location.protocol)) {
    return new URL(url, window.location.href).toString();
  }

  return url;
}

function isSafeDownloadURL(url: string): boolean {
  return /^(https?:|blob:)/i.test(String(url || "").trim());
}

function triggerBrowserDownload(url: string, filename: string): void {
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  anchor.rel = "noopener";
  anchor.style.display = "none";
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
}

async function blobToBase64(blob: Blob): Promise<string> {
  const bytes = new Uint8Array(await blob.arrayBuffer());
  let binary = "";
  const chunkSize = 0x8000;
  for (let offset = 0; offset < bytes.length; offset += chunkSize) {
    const chunk = bytes.subarray(offset, offset + chunkSize);
    binary += String.fromCharCode(...chunk);
  }
  return btoa(binary);
}

async function downloadBlobWithNativeShell(blob: Blob, filename: string): Promise<void> {
  const data = await blobToBase64(blob);
  const unifiedBridge = getNativeBridge();
  if (typeof unifiedBridge?.downloadBase64 === "function") {
    const result = await unifiedBridge.downloadBase64(JSON.stringify({ filename, data }));
    if (typeof result === "string" && result) {
      throw new Error(result);
    }
    return;
  }

  const nativeBridge = (window as WindowWithNativeDownloadBridge).MindFSNativeDownload;
  if (typeof nativeBridge?.downloadBase64 === "function") {
    const errorMessage = nativeBridge.downloadBase64(data, filename);
    if (errorMessage) {
      throw new Error(errorMessage);
    }
    return;
  }

  try {
    await NativeDownload.saveBase64({ data, filename });
    return;
  } catch (error) {
    console.warn("[download] native saveBase64 plugin unavailable", error);
  }

  await Filesystem.writeFile({
    path: filename,
    data,
    directory: Directory.Documents,
  });
}

async function downloadBlob(blob: Blob, filename: string): Promise<void> {
  if (isNativeShellRuntime()) {
    await downloadBlobWithNativeShell(blob, filename);
    return;
  }
  if (typeof URL === "undefined" || typeof URL.createObjectURL !== "function") {
    throw new Error("当前浏览器不支持安全下载");
  }
  const objectURL = URL.createObjectURL(blob);
  try {
    triggerBrowserDownload(objectURL, filename);
  } finally {
    window.setTimeout(() => URL.revokeObjectURL(objectURL), 30_000);
  }
}

async function downloadWithNativeShell(url: string, filename: string): Promise<void> {
  if (!/^https?:\/\//i.test(url)) {
    throw new Error("下载地址不是完整的 http/https URL，请先配置移动端 API 地址");
  }

  const unifiedBridge = getNativeBridge();
  if (typeof unifiedBridge?.download === "function") {
    const result = await unifiedBridge.download(JSON.stringify({ url, filename }));
    if (typeof result === "string" && result) {
      throw new Error(result);
    }
    return;
  }

  const nativeBridge = (window as WindowWithNativeDownloadBridge).MindFSNativeDownload;
  if (nativeBridge && typeof nativeBridge.download === "function") {
    const errorMessage = nativeBridge.download(url, filename);
    if (errorMessage) {
      throw new Error(errorMessage);
    }
    return;
  }

  await NativeDownload.download({ url, filename });
}

export async function downloadURL(url: string, filename = "download"): Promise<void> {
  if (typeof document === "undefined") {
    throw new Error("download is only available in browser runtime");
  }

  const safeFilename = sanitizeDownloadName(filename, filename);
  const absoluteURL = toAbsoluteDownloadURL(url);
  if (!isSafeDownloadURL(absoluteURL)) {
    throw new Error("下载地址必须是 http/https/blob URL");
  }
  if (isNativeShellRuntime()) {
    await downloadWithNativeShell(absoluteURL, safeFilename);
    return;
  }

  triggerBrowserDownload(absoluteURL, safeFilename);
}

export async function downloadFile(params: DownloadFileParams): Promise<void> {
  const filename = sanitizeDownloadName(params.path, params.name);
  if (e2eeService.isRequired()) {
    const blob = await fetchProofProtectedBlob({
      rootId: params.rootId,
      path: params.path,
    });
    await downloadBlob(blob, filename);
    return;
  }
  const url = toAbsoluteDownloadURL(buildDownloadURL(params.rootId, params.path));
  await downloadURL(url, filename);
}
