import { buildApiUrl, readApiResponse } from "@/lib/api/core";

export async function clientApiFetch<T>(pathname: string, init?: RequestInit) {
  const headers = new Headers(init?.headers);
  headers.set("Accept", "application/json");

  const response = await fetch(buildApiUrl(pathname), {
    ...init,
    headers,
    credentials: "include",
    cache: "no-store",
  });

  return readApiResponse<T>(response);
}

type UploadOptions = {
  url: string;
  file: File;
  onProgress?: (loadedBytes: number) => void;
  signal?: AbortSignal;
};

export function uploadFileWithProgress({ url, file, onProgress, signal }: UploadOptions) {
  return new Promise<void>((resolve, reject) => {
    const xhr = new XMLHttpRequest();

    xhr.upload.addEventListener("progress", (event) => {
      if (!event.lengthComputable) {
        return;
      }
      onProgress?.(event.loaded);
    });

    xhr.addEventListener("load", () => {
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve();
        return;
      }

      reject(new Error(`Upload failed with status ${xhr.status}`));
    });

    xhr.addEventListener("error", () => {
      reject(new Error("Upload failed"));
    });

    xhr.addEventListener("abort", () => {
      reject(new DOMException("Upload aborted", "AbortError"));
    });

    const abortUpload = () => xhr.abort();
    signal?.addEventListener("abort", abortUpload, { once: true });

    xhr.open("PUT", url);
    if (file.type) {
      xhr.setRequestHeader("Content-Type", file.type);
    }
    xhr.send(file);
  });
}
