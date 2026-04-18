import Link from "next/link";

export default function NotFound() {
  return (
    <main className="flex min-h-screen items-center justify-center px-6 py-16">
      <div className="ambient-panel max-w-xl rounded-[var(--radius-panel)] border border-white/50 px-8 py-10 text-left">
        <p className="text-sm uppercase tracking-[0.32em] text-[var(--color-text-soft)]">
          Archive Missing
        </p>
        <h1 className="font-heading mt-4 text-4xl font-semibold tracking-tight text-[var(--color-text)]">
          This part of the archive does not exist.
        </h1>
        <p className="mt-4 max-w-md text-[15px] leading-7 text-[var(--color-text-muted)]">
          The bucket or path you opened is not available in the current mock archive.
        </p>
        <Link
          href="/archive/my-archive"
          className="primary-gradient mt-8 inline-flex rounded-full px-5 py-3 text-sm font-semibold text-white"
        >
          Return to My Archive
        </Link>
      </div>
    </main>
  );
}
