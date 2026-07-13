import { appURL } from "./base";
import { E2EE_HEADER, e2eeService, type CipherEnvelope } from "./e2ee";

export type UploadedFile = {
  path: string;
  name: string;
  mime: string;
  size: number;
};

type UploadResponse = {
  files?: UploadedFile[];
};

type ProtectedUploadFile = {
  name: string;
  content_type: string;
  envelope: CipherEnvelope;
};

type ProtectedUploadRequest = {
  dir?: string;
  files: ProtectedUploadFile[];
};

async function buildProtectedUploadRequest(params: {
  files: File[];
  dir?: string;
}): Promise<ProtectedUploadRequest> {
  const files = await Promise.all(
    params.files.map(async (file) => ({
      name: file.name,
      content_type: file.type || "application/octet-stream",
      envelope: await e2eeService.encryptEnvelopeBytes(new Uint8Array(await file.arrayBuffer())),
    })),
  );
  return {
    dir: params.dir || "",
    files,
  };
}

export async function uploadFiles(params: {
  rootId: string;
  files: File[];
  dir?: string;
}): Promise<UploadedFile[]> {
  const query = new URLSearchParams({ root: params.rootId });
  const requestURL = appURL("/api/upload", query);
  let headers: HeadersInit | undefined;
  let body: BodyInit;
  if (e2eeService.isRequired()) {
    headers = await e2eeService.fileProofHeaders("POST", requestURL, {
      "Content-Type": "application/json",
      [E2EE_HEADER]: "1",
    });
    body = JSON.stringify(await buildProtectedUploadRequest(params));
  } else {
    const formData = new FormData();
    params.files.forEach((file) => {
      formData.append("files", file);
    });
    if (params.dir) {
      formData.append("dir", params.dir);
    }
    body = formData;
  }
  const response = await fetch(requestURL, {
    method: "POST",
    headers,
    body,
  });
  if (headers) {
    e2eeService.bindProtectedResponse(response, headers);
  }
  if (!response.ok) {
    if (response.status === 401 && e2eeService.isRequired()) {
      const payload = (await response.json().catch(() => ({}))) as { error?: string };
      if (e2eeService.handleServerError(String(payload.error || ""))) {
        return uploadFiles(params);
      }
      throw new Error(payload.error || `Upload failed: ${response.status}`);
    }
    let message = `Upload failed: ${response.status}`;
    try {
      const payload = (await response.json()) as { error?: string };
      if (payload?.error) {
        message = payload.error;
      }
    } catch {
    }
    throw new Error(message);
  }
  const payload = await e2eeService.parseProtectedJSONResponse<UploadResponse>(response);
  return Array.isArray(payload.files) ? payload.files : [];
}
