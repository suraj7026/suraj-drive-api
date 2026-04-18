"use client";

import { useMemo, useState } from "react";
import { CloudUpload, Pause, Play, X } from "lucide-react";
import { AppShell } from "@/components/shell/app-shell";
import { TransferDrawer, useAnimatedTransfers } from "@/components/upload/transfer-drawer";
import type { UploadScreenData } from "@/lib/models/transfers";
import { formatBytes } from "@/lib/utils/format";

export function UploadManagerView({ uploadData }: { uploadData: UploadScreenData }) {
  const [selectedId, setSelectedId] = useState(uploadData.transfers[0]?.id ?? null);
  const transfers = useAnimatedTransfers(uploadData.transfers);
  const selected = useMemo(
    () => transfers.find((transfer) => transfer.id === selectedId) ?? transfers[0] ?? null,
    [selectedId, transfers]
  );

  return (
    <AppShell
      eyebrow="Upload Manager"
      title="Syncing files to My Archive"
      detail={
        selected ? (
          <div className="space-y-4">
            <p className="text-xs uppercase tracking-[0.3em] text-[var(--color-text-soft)]">Active Transfer</p>
            <h3 className="font-heading text-2xl font-semibold tracking-[-0.04em]">{selected.fileName}</h3>
            <p className="text-sm text-[var(--color-text-muted)]">
              {formatBytes(selected.transferredBytes)} of {formatBytes(selected.totalBytes)} archived into {selected.targetLabel}
            </p>
          </div>
        ) : null
      }
      transferDrawer={<TransferDrawer initialTransfers={uploadData.transfers} />}
    >
      <div className="grid gap-6 xl:grid-cols-[0.95fr_1.05fr]">
        <section className="grid gap-6">
          <div className="rounded-[36px] bg-[var(--color-surface-low)] p-7">
            <p className="text-xs uppercase tracking-[0.28em] text-[var(--color-text-soft)]">Dropzone</p>
            <div className="mt-6 rounded-[32px] bg-[var(--color-surface-strong)] px-6 py-12 text-center shadow-[inset_0_0_0_1px_var(--color-outline)]">
              <div className="mx-auto flex h-16 w-16 items-center justify-center rounded-[22px] bg-[var(--color-surface-low)] text-[var(--color-primary)]">
                <CloudUpload size={28} />
              </div>
              <h2 className="font-heading mt-6 text-3xl font-semibold tracking-[-0.04em]">Drag and drop files</h2>
              <p className="mx-auto mt-4 max-w-md text-sm leading-7 text-[var(--color-text-muted)]">
                Support for RAW, MP4, PDF, and high-resolution assets up to 50GB. The interactions are mocked but ready for backend upload wiring later.
              </p>
              <button className="primary-gradient mt-8 rounded-full px-5 py-3 text-sm font-semibold text-white">
                Browse Local Files
              </button>
            </div>
          </div>

          <div className="rounded-[36px] bg-[var(--color-surface-strong)] p-6 shadow-[0_12px_32px_rgba(26,28,25,0.06)]">
            <p className="text-xs uppercase tracking-[0.28em] text-[var(--color-text-soft)]">Queue Summary</p>
            <div className="mt-5 grid gap-4 sm:grid-cols-3">
              {uploadData.summary.map((item) => (
                <div key={item.label} className="rounded-[24px] bg-[var(--color-surface-low)] px-4 py-5">
                  <p className="text-xs uppercase tracking-[0.24em] text-[var(--color-text-soft)]">{item.label}</p>
                  <p className="font-heading mt-4 text-3xl font-semibold tracking-[-0.04em]">{item.value}</p>
                </div>
              ))}
            </div>
          </div>
        </section>

        <section className="rounded-[36px] bg-[var(--color-surface-strong)] p-6 shadow-[0_12px_32px_rgba(26,28,25,0.06)]">
          <div className="flex items-center justify-between gap-3">
            <div>
              <p className="text-xs uppercase tracking-[0.28em] text-[var(--color-text-soft)]">Active Transfers</p>
              <h2 className="font-heading mt-2 text-2xl font-semibold tracking-[-0.04em]">
                {transfers.length} items in motion
              </h2>
            </div>
            <div className="flex gap-2">
              <CircleButton icon={Pause} label="Pause All" />
              <CircleButton icon={Play} label="Resume All" />
            </div>
          </div>

          <div className="mt-6 grid gap-4">
            {transfers.map((transfer) => {
              const percent = Math.min(100, Math.round((transfer.transferredBytes / transfer.totalBytes) * 100));

              return (
                <button
                  key={transfer.id}
                  onClick={() => setSelectedId(transfer.id)}
                  className="rounded-[26px] bg-[var(--color-surface-low)] px-5 py-5 text-left transition-transform duration-200 hover:-translate-y-0.5"
                >
                  <div className="flex items-start justify-between gap-4">
                    <div>
                      <p className="text-[15px] font-medium text-[var(--color-text)]">{transfer.fileName}</p>
                      <p className="mt-1 text-sm text-[var(--color-text-soft)]">{transfer.statusLabel}</p>
                    </div>
                    <button className="rounded-full bg-[var(--color-surface-strong)] p-2 text-[var(--color-text-soft)]">
                      <X size={16} />
                    </button>
                  </div>

                  <div className="mt-5 h-2 rounded-full bg-white/70">
                    <div
                      className="primary-gradient h-2 rounded-full transition-[width] duration-700"
                      style={{ width: `${percent}%` }}
                    />
                  </div>

                  <div className="mt-4 flex items-center justify-between text-sm text-[var(--color-text-muted)]">
                    <span>{percent}%</span>
                    <span>
                      {formatBytes(transfer.transferredBytes)} of {formatBytes(transfer.totalBytes)}
                    </span>
                  </div>
                </button>
              );
            })}
          </div>
        </section>
      </div>
    </AppShell>
  );
}

function CircleButton({ icon: Icon, label }: { icon: typeof Pause; label: string }) {
  return (
    <button
      aria-label={label}
      className="flex h-11 w-11 items-center justify-center rounded-full bg-[var(--color-surface-low)] text-[var(--color-text-soft)] transition-colors hover:text-[var(--color-text)]"
    >
      <Icon size={16} />
    </button>
  );
}
