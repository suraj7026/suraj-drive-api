const DEFAULT_API_URL = "http://localhost:4001";

export class ApiError extends Error {
  constructor(
    message: string,
    public readonly status: number,
    public readonly payload?: unknown
  ) {
    super(message);
    this.name = "ApiError";
  }
}

export function getApiBaseUrl() {
  const configured = process.env.API_URL ?? process.env.NEXT_PUBLIC_API_URL ?? DEFAULT_API_URL;
  return configured.endsWith("/") ? configured.slice(0, -1) : configured;
}

export function buildApiUrl(pathname: string, query?: Record<string, string | number | undefined>) {
  const url = new URL(pathname, getApiBaseUrl());

  for (const [key, value] of Object.entries(query ?? {})) {
    if (value === undefined) {
      continue;
    }
    url.searchParams.set(key, String(value));
  }

  return url.toString();
}

export async function readApiResponse<T>(response: Response): Promise<T> {
  const text = await response.text();
  const payload = text ? (JSON.parse(text) as unknown) : undefined;

  if (!response.ok) {
    const message = readErrorMessage(payload) ?? `Request failed with status ${response.status}`;
    throw new ApiError(message, response.status, payload);
  }

  return payload as T;
}

function readErrorMessage(payload: unknown) {
  if (payload && typeof payload === "object" && "error" in payload && typeof payload.error === "string") {
    return payload.error;
  }

  return null;
}
