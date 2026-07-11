type AndroidMessagePort = {
  postMessage: (message: string) => void;
  addEventListener: (type: "message", listener: (event: MessageEvent<string>) => void) => void;
};

type NativeResponse = {
  requestId: string;
  ok: boolean;
  result?: unknown;
  error?: string;
};

type NativeContactEmail = {
  name: string;
  email: string;
};

type NativeSharedFile = {
  name: string;
  type: string;
  size: number;
  url: string;
};

type NativeShareManifest = {
  shareId: string;
  files: NativeSharedFile[];
};

declare global {
  interface Window {
    RolltopAndroid?: AndroidMessagePort;
  }
}

const pendingRequests = new Map<string, { resolve: (value: unknown) => void; reject: (error: Error) => void }>();
const shareLoads = new Map<string, Promise<File[]>>();
const MAX_SHARED_FILE_COUNT = 32;
const MAX_SHARED_UPLOAD_BYTES = 80 * 1024 * 1024;
let attachedPort: AndroidMessagePort | null = null;
let requestSequence = 1;

export function androidNativeAvailable(): boolean {
  return Boolean(window.RolltopAndroid);
}

export async function pickAndroidContactEmail(): Promise<NativeContactEmail | null> {
  const result = await nativeRequest("pickContactEmail", {});
  if (!result || typeof result !== "object") return null;
  const contact = result as Partial<NativeContactEmail>;
  if (typeof contact.email !== "string" || !contact.email.trim()) return null;
  return {
    name: typeof contact.name === "string" ? contact.name.trim() : "",
    email: contact.email.trim()
  };
}

export function loadAndroidSharedFiles(shareId: string): Promise<File[]> {
  const normalized = shareId.trim();
  if (!normalized || !androidNativeAvailable()) return Promise.resolve([]);
  const existing = shareLoads.get(normalized);
  if (existing) return existing;
  const load = loadSharedFiles(normalized);
  shareLoads.set(normalized, load);
  void load.then(
    () => shareLoads.delete(normalized),
    () => shareLoads.delete(normalized)
  );
  return load;
}

async function loadSharedFiles(shareId: string): Promise<File[]> {
  try {
    const result = await nativeRequest("sharedFiles", { shareId });
    const manifest = result as Partial<NativeShareManifest> | null;
    if (!manifest || !Array.isArray(manifest.files)) throw new Error("Android did not provide the shared files.");
    if (manifest.shareId !== shareId) throw new Error("Android returned the wrong shared-file session.");
    if (manifest.files.length > MAX_SHARED_FILE_COUNT) {
      throw new Error(`Share at most ${MAX_SHARED_FILE_COUNT} files at once.`);
    }
    const files: File[] = [];
    let loadedBytes = 0;
    for (const item of manifest.files) {
      if (!validSharedFile(item, shareId)) throw new Error("Android returned an invalid shared file.");
      const remainingBytes = MAX_SHARED_UPLOAD_BYTES - loadedBytes;
      if (item.size > remainingBytes) throw new Error("The shared files exceed Rolltop's 80 MB upload limit.");
      let response: Response;
      try {
        response = await fetch(item.url, { cache: "no-store", credentials: "omit" });
      } catch {
        throw new Error(`Android could not provide ${item.name}. Close this draft and share the file again.`);
      }
      if (!response.ok) throw new Error(`Could not read ${item.name}.`);
      const blob = await readBoundedBlob(response, remainingBytes, item.name);
      loadedBytes += blob.size;
      files.push(new File([blob], item.name || "attachment", {
        type: item.type || blob.type || "application/octet-stream",
        lastModified: Date.now()
      }));
    }
    return files;
  } finally {
    removeShareParameter(shareId);
    await nativeRequest("releaseShare", { shareId }).catch(() => undefined);
  }
}

function nativeRequest(action: string, payload: Record<string, unknown>): Promise<unknown> {
  const port = window.RolltopAndroid;
  if (!port) return Promise.reject(new Error("The Rolltop Android bridge is unavailable."));
  attachResponseListener(port);
  const requestId = `${Date.now()}-${requestSequence++}`;
  return new Promise((resolve, reject) => {
    pendingRequests.set(requestId, { resolve, reject });
    try {
      port.postMessage(JSON.stringify({ requestId, action, ...payload }));
    } catch (error) {
      pendingRequests.delete(requestId);
      reject(error instanceof Error ? error : new Error("Could not contact Android."));
    }
  });
}

function attachResponseListener(port: AndroidMessagePort) {
  if (attachedPort === port) return;
  attachedPort = port;
  port.addEventListener("message", (event) => {
    let response: NativeResponse;
    try {
      response = JSON.parse(String(event.data)) as NativeResponse;
    } catch {
      return;
    }
    const pending = pendingRequests.get(response.requestId);
    if (!pending) return;
    pendingRequests.delete(response.requestId);
    if (response.ok) pending.resolve(response.result);
    else pending.reject(new Error(response.error || "Android could not complete the request."));
  });
}

async function readBoundedBlob(response: Response, maxBytes: number, name: string): Promise<Blob> {
  const declaredLength = Number(response.headers.get("Content-Length"));
  if (Number.isFinite(declaredLength) && declaredLength > maxBytes) {
    throw new Error(`The shared file ${name} exceeds Rolltop's upload limit.`);
  }
  if (!response.body) {
    const blob = await response.blob();
    if (blob.size > maxBytes) throw new Error(`The shared file ${name} exceeds Rolltop's upload limit.`);
    return blob;
  }

  const reader = response.body.getReader();
  const chunks: ArrayBuffer[] = [];
  let size = 0;
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      if (!value) continue;
      size += value.byteLength;
      if (size > maxBytes) {
        await reader.cancel();
        throw new Error(`The shared file ${name} exceeds Rolltop's upload limit.`);
      }
      const chunk = new Uint8Array(value.byteLength);
      chunk.set(value);
      chunks.push(chunk.buffer);
    }
  } finally {
    reader.releaseLock();
  }
  return new Blob(chunks, { type: response.headers.get("Content-Type") || "application/octet-stream" });
}

function validSharedFile(value: unknown, shareId: string): value is NativeSharedFile {
  if (!value || typeof value !== "object") return false;
  const item = value as Partial<NativeSharedFile>;
  if (
    typeof item.url !== "string" ||
    typeof item.name !== "string" ||
    typeof item.type !== "string" ||
    typeof item.size !== "number" ||
    !Number.isFinite(item.size) ||
    item.size < -1
  ) return false;
  try {
    const url = new URL(item.url);
    return url.origin === window.location.origin &&
      url.pathname.startsWith(`/rolltop-native-share/${shareId}/`);
  } catch {
    return false;
  }
}

function removeShareParameter(shareId: string) {
  const url = new URL(window.location.href);
  if (url.searchParams.get("android_share") !== shareId) return;
  url.searchParams.delete("android_share");
  window.history.replaceState(window.history.state, "", `${url.pathname}${url.search}${url.hash}`);
}
