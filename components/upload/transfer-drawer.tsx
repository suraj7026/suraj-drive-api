"use client";

import { useMemo, useState } from "react";
import { CloudUpload, Pause, Play, TriangleAlert, X } from "lucide-react";
import type { TransferItem } from "@/lib/models/transfers";
import { formatBytes } from "@/lib/utils/format";

type TransferDrawerProps = {
  transfers: TransferItem[];
  onToggleStatus?: (transferId: string) => void;
  onRemove?: (transferId: string) => void;
};

export function TransferDrawer({ transfers, onToggleStatus, onRemove }: TransferDrawerProps) {
  const [open, setOpen] = useState(true);

  const summary = useMemo(() => {
    const active = transfers.filter((transfer) => transfer.status === "uploading").length;
    return { active, total: transfers.length };
  }, [transfers]);

  return (
    <div className="fixed bottom-5 right-5 z-20 hidden w-[360px] xl:block">
      <div className="ambient-panel overflow-hidden rounded-[32px]">
        <button
          type="button"
          onClick={() => setOpen((current) => !current)}
          className="flex w-full items-center justify-between px-5 py-4 text-left"
        >
          <span>
            <span className="text-xs uppercase tracking-[0.28em] text-[var(--color-text-soft)]">Transfer Drawer</span>
            <span className="font-heading mt-1 block text-lg font-semibold tracking-[-0.04em]">
              {summary.active} active of {summary.total}
            </span>
          </span>
          <span className="flex h-10 w-10 items-center justify-center rounded-full bg-[var(--color-surface-strong)] text-[var(--color-primary)]">
            <CloudUpload size={18} />
          </span>
        </button>

        {open ? (
          <div className="grid gap-3 px-4 py-4">
            {transfers.length === 0 ? (
              <div className="rounded-[24px] bg-[var(--color-surface-strong)] px-4 py-5 text-sm text-[var(--color-text-soft)] shadow-[0_12px_32px_rgba(26,28,25,0.05)]">
                No active transfers yet.
              </div>
            ) : null}

            {transfers.slice(0, 3).map((transfer) => {
              const percent = transfer.totalBytes > 0
                ? Math.min(100, Math.round((transfer.transferredBytes / transfer.totalBytes) * 100))
                : 0;

              return (
                <div key={transfer.id} className="rounded-[24px] bg-[var(--color-surface-strong)] px-4 py-4 shadow-[0_12px_32px_rgba(26,28,25,0.05)]">
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0">
                      <p className="truncate text-sm font-medium">{transfer.fileName}</p>
                      <p className="mt-1 text-xs text-[var(--color-text-soft)]">{transfer.statusLabel}</p>
                    </div>
                    <div className="flex gap-1">
                      <button
                        type="button"
                        disabled={!onToggleStatus || transfer.status === "done" || transfer.status === "error"}
                        onClick={() => onToggleStatus?.(transfer.id)}
                        className="rounded-full p-2 text-[var(--color-text-soft)] hover:bg-[var(--color-surface-low)] disabled:cursor-not-allowed disabled:opacity-40"
                      >
                        {transfer.status === "paused" ? <Play size={14} /> : <Pause size={14} />}
                      </button>
                      <button
                        type="button"
                        disabled={!onRemove}
                        onClick={() => onRemove?.(transfer.id)}
                        className="rounded-full p-2 text-[var(--color-text-soft)] hover:bg-[var(--color-surface-low)] disabled:cursor-not-allowed disabled:opacity-40"
                      >
                        <X size={14} />
                      </button>
                    </div>
                  </div>

                  {transfer.status === "error" ? (
                    <div className="mt-4 flex items-start gap-2 rounded-[18px] bg-red-50 px-3 py-3 text-xs text-red-700">
                      <TriangleAlert size={14} className="mt-0.5 shrink-0" />
                      <span>{transfer.errorMessage ?? "Upload failed."}</span>
                    </div>
                  ) : (
                    <>
                      <div className="mt-4 h-2 rounded-full bg-[var(--color-surface-low)]">
                        <div className="primary-gradient h-2 rounded-full transition-[width] duration-300" style={{ width: `${percent}%` }} />
                      </div>
                      <div className="mt-3 flex items-center justify-between text-xs text-[var(--color-text-soft)]">
                        <span>{percent}%</span>
                        <span>
                          {formatBytes(transfer.transferredBytes)} / {formatBytes(transfer.totalBytes)}
                        </span>
                      </div>
                    </>
                  )}
                </div>
              );
            })}
          </div>
        ) : null}
      </div>
    </div>
  );
}
