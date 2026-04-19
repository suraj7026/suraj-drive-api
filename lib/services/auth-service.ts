import { redirect } from "next/navigation";
import { ApiError } from "@/lib/api/core";
import { serverApiFetch } from "@/lib/api/server";
import type { CurrentUser } from "@/lib/models/auth";

export async function getCurrentUser(): Promise<CurrentUser | null> {
  try {
    return await serverApiFetch<CurrentUser>("/api/auth/me");
  } catch (error) {
    if (error instanceof ApiError && error.status === 401) {
      return null;
    }

    throw error;
  }
}

export async function requireCurrentUser() {
  const user = await getCurrentUser();
  if (!user) {
    redirect("/login");
  }

  return user;
}
