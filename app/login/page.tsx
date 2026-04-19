import Link from "next/link";
import { redirect } from "next/navigation";
import { getApiBaseUrl } from "@/lib/api/core";
import { getCurrentUser } from "@/lib/services/auth-service";

export default async function LoginPage() {
  const user = await getCurrentUser();
  if (user) {
    redirect(`/archive/${user.bucket}`);
  }

  const loginHref = `${getApiBaseUrl()}/api/auth/google/login`;

  return (
    <main className="flex min-h-screen items-center justify-center px-6 py-16">
      <div className="ambient-panel max-w-2xl rounded-[36px] px-8 py-10 sm:px-12 sm:py-12">
        <p className="text-sm uppercase tracking-[0.32em] text-[var(--color-text-soft)]">Connected Archive</p>
        <h1 className="font-heading mt-4 text-4xl font-semibold tracking-[-0.05em] text-[var(--color-text)] sm:text-5xl">
          Sign in to open your personal bucket.
        </h1>
        <p className="mt-5 max-w-xl text-[15px] leading-7 text-[var(--color-text-muted)]">
          This frontend now reads directly from the Go backend. Google auth provisions your bucket, stores a secure session cookie, and unlocks file browsing, search, uploads, downloads, copy, and delete actions.
        </p>

        <div className="mt-8 flex flex-wrap items-center gap-3">
          <a href={loginHref} className="primary-gradient inline-flex rounded-full px-5 py-3 text-sm font-semibold text-white">
            Continue with Google
          </a>
          <Link href="/" className="rounded-full bg-[var(--color-surface-low)] px-5 py-3 text-sm font-medium text-[var(--color-text)]">
            Back home
          </Link>
        </div>
      </div>
    </main>
  );
}
