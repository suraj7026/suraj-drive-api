"use client";

import { useEffect, useMemo, useState } from "react";
import { CloudUpload, Pause, Play, X } from "lucide-react";
import type { TransferItem } from "@/lib/models/transfers";
import { formatBytes } from "@/lib/utils/format";

export function useAnimatedTransfers(initialTransfers: TransferItem[]) {
  const [transfers, setTransfers] = useState(initialTransfers);

  useEffect(() => {
    const interval = window.setInterval(() => {
      setTransfers((current) =>
        current.map((transfer) => {
          if (transfer.status !== "uploading") {
            return transfer;
          }

          const next = Math.min(transfer.totalBytes, transfer.transferredBytes + transfer.tickBytes);

          return {
            ...transfer,
            transferredBytes: next,
            status: next >= transfer.totalBytes ? "done" : transfer.status,
            statusLabel: next >= transfer.totalBytes ? "Completed" : transfer.statusLabel,
          };
        })
      );
    }, 1200);

    return () => window.clearInterval(interval);
  }, []);

  return transfers;
}

export function TransferDrawer({ initialTransfers }: { initialTransfers: TransferItem[] }) {
  const [open, setOpen] = useState(true);
  const transfers = useAnimatedTransfers(initialTransfers);

  const summary = useMemo(() => {
    const active = transfers.filter((transfer) => transfer.status === "uploading").length;
    return { active, total: transfers.length };
  }, [transfers]);

  return (
    <div className="fixed bottom-5 right-5 z-20 hidden w-[360px] xl:block">
      <div className="ambient-panel overflow-hidden rounded-[32px]">
        <button
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
            {transfers.slice(0, 3).map((transfer) => {
              const percent = Math.min(100, Math.round((transfer.transferredBytes / transfer.totalBytes) * 100));

              return (
                <div key={transfer.id} className="rounded-[24px] bg-[var(--color-surface-strong)] px-4 py-4 shadow-[0_12px_32px_rgba(26,28,25,0.05)]">
                  <div className="flex items-start justify-between gap-3">
                    <div>
                      <p className="text-sm font-medium">{transfer.fileName}</p>
                      <p className="mt-1 text-xs text-[var(--color-text-soft)]">{transfer.statusLabel}</p>
                    </div>
                    <div className="flex gap-1">
                      <button className="rounded-full p-2 text-[var(--color-text-soft)] hover:bg-[var(--color-surface-low)]">
                        {transfer.status === "uploading" ? <Pause size={14} /> : <Play size={14} />}
                      </button>
                      <button className="rounded-full p-2 text-[var(--color-text-soft)] hover:bg-[var(--color-surface-low)]">
                        <X size={14} />
                      </button>
                    </div>
                  </div>
                  <div className="mt-4 h-2 rounded-full bg-[var(--color-surface-low)]">
                    <div className="primary-gradient h-2 rounded-full transition-[width] duration-700" style={{ width: `${percent}%` }} />
                  </div>
                  <div className="mt-3 flex items-center justify-between text-xs text-[var(--color-text-soft)]">
                    <span>{percent}%</span>
                    <span>
                      {formatBytes(transfer.transferredBytes)} / {formatBytes(transfer.totalBytes)}
                    </span>
                  </div>
                </div>
              );
            })}
          </div>
        ) : null}
      </div>
    </div>
  );
}
