import { cookies } from "next/headers";
import { buildApiUrl, readApiResponse } from "@/lib/api/core";

export async function serverApiFetch<T>(pathname: string, init?: RequestInit) {
  const headers = new Headers(init?.headers);
  const cookieStore = await cookies();
  const cookieHeader = cookieStore
    .getAll()
    .map((cookie) => `${cookie.name}=${cookie.value}`)
    .join("; ");

  headers.set("Accept", "application/json");
  if (cookieHeader) {
    headers.set("Cookie", cookieHeader);
  }

  const response = await fetch(buildApiUrl(pathname), {
    ...init,
    headers,
    cache: "no-store",
  });

  return readApiResponse<T>(response);
}
