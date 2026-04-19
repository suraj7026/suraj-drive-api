"use client";

import { useMemo, useRef, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { CloudUpload, Pause, Play, TriangleAlert, X } from "lucide-react";
import { clientApiFetch, uploadFileWithProgress } from "@/lib/api/client";
import type { BackendPresignResponse } from "@/lib/models/backend";
import { AppShell } from "@/components/shell/app-shell";
import { TransferDrawer } from "@/components/upload/transfer-drawer";
import type { TransferItem, UploadScreenData } from "@/lib/models/transfers";
import { formatBytes } from "@/lib/utils/format";

export function UploadManagerView({ uploadData }: { uploadData: UploadScreenData }) {
  const router = useRouter();
  const inputRef = useRef<HTMLInputElement | null>(null);
  const controllersRef = useRef<Record<string, AbortController>>({});
  const [selectedId, setSelectedId] = useState<string | null>(uploadData.transfers[0]?.id ?? null);
  const [transfers, setTransfers] = useState(uploadData.transfers);
  const [statusMessage, setStatusMessage] = useState<string | null>(null);
  const [dropActive, setDropActive] = useState(false);

  const selected = useMemo(
    () => transfers.find((transfer) => transfer.id === selectedId) ?? transfers[0] ?? null,
    [selectedId, transfers]
  );

  const summary = useMemo(() => {
    const largestFile = transfers.reduce((largest, transfer) => Math.max(largest, transfer.totalBytes), 0);

    return [
      { label: "Queue Depth", value: `${transfers.length} ${transfers.length === 1 ? "Item" : "Items"}` },
      { label: "Destination", value: uploadData.targetLabel },
      { label: "Largest File", value: largestFile > 0 ? formatBytes(largestFile) : "--" },
    ];
  }, [transfers, uploadData.targetLabel]);

  async function handleFilesSelected(files: FileList | null) {
    if (!files || files.length === 0) {
      return;
    }

    const selectedFiles = Array.from(files);
    setStatusMessage(null);

    for (const file of selectedFiles) {
      const transferId = buildTransferId(file);
      setTransfers((current) => [
        {
          id: transferId,
          fileName: file.name,
          totalBytes: file.size,
          transferredBytes: 0,
          status: "queued",
          statusLabel: "Waiting for upload slot...",
          targetLabel: uploadData.targetLabel,
        },
        ...current,
      ]);
      setSelectedId((current) => current ?? transferId);
      void uploadTransfer(transferId, file);
    }

    if (inputRef.current) {
      inputRef.current.value = "";
    }
  }

  async function uploadTransfer(transferId: string, file: File) {
    const requestedKey = uploadData.targetPrefix ? `${uploadData.targetPrefix}/${file.name}` : file.name;
    const controller = new AbortController();
    controllersRef.current[transferId] = controller;

    try {
      updateTransfer(transferId, {
        status: "uploading",
        statusLabel: `Preparing upload to ${uploadData.targetLabel}`,
      });

      const presign = await clientApiFetch<BackendPresignResponse>(`/api/files/presign/upload?key=${encodeURIComponent(requestedKey)}`);

      updateTransfer(transferId, {
        objectKey: presign.key,
        statusLabel: `Uploading to ${uploadData.targetLabel}`,
      });

      await uploadFileWithProgress({
        url: presign.url,
        file,
        signal: controller.signal,
        onProgress: (loadedBytes) => {
          updateTransfer(transferId, {
            transferredBytes: loadedBytes,
            status: "uploading",
            statusLabel: `${formatBytes(loadedBytes)} of ${formatBytes(file.size)} uploaded`,
          });
        },
      });

      updateTransfer(transferId, {
        transferredBytes: file.size,
        status: "done",
        statusLabel: `Uploaded to ${uploadData.targetLabel}`,
      });
      setStatusMessage(`Uploaded ${file.name}`);
      void router.prefetch(`/archive/${uploadData.user.bucket}`);
    } catch (error) {
      const aborted = error instanceof DOMException && error.name === "AbortError";
      updateTransfer(transferId, {
        status: aborted ? "paused" : "error",
        statusLabel: aborted ? "Upload canceled" : "Upload failed",
        errorMessage: aborted ? undefined : error instanceof Error ? error.message : "Upload failed",
      });
    } finally {
      delete controllersRef.current[transferId];
    }
  }

  function updateTransfer(transferId: string, updates: Partial<TransferItem>) {
    setTransfers((current) => current.map((transfer) => (transfer.id === transferId ? { ...transfer, ...updates } : transfer)));
  }

  function handleRemoveTransfer(transferId: string) {
    controllersRef.current[transferId]?.abort();
    delete controllersRef.current[transferId];
    setTransfers((current) => current.filter((transfer) => transfer.id !== transferId));
    setSelectedId((current) => (current === transferId ? null : current));
  }

  function handleToggleTransfer(transferId: string) {
    const transfer = transfers.find((candidate) => candidate.id === transferId);
    if (!transfer) {
      return;
    }

    if (transfer.status === "uploading") {
      controllersRef.current[transferId]?.abort();
      return;
    }

    if (transfer.status === "paused") {
      setStatusMessage("Canceled uploads need to be added again before they can resume.");
    }
  }

  return (
    <AppShell
      user={uploadData.user}
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
            <Link href={`/archive/${uploadData.user.bucket}`} className="text-sm font-medium text-[var(--color-primary)]">
              Open archive
            </Link>
          </div>
        ) : (
          <div className="space-y-3 text-sm text-[var(--color-text-soft)]">
            <p>No uploads are running right now.</p>
            <Link href={`/archive/${uploadData.user.bucket}`} className="font-medium text-[var(--color-primary)]">
              Return to archive
            </Link>
          </div>
        )
      }
      transferDrawer={
        <TransferDrawer transfers={transfers} onToggleStatus={handleToggleTransfer} onRemove={handleRemoveTransfer} />
      }
    >
      <div className="grid gap-6 xl:grid-cols-[0.95fr_1.05fr]">
        <section className="grid gap-6">
          <div className="rounded-[36px] bg-[var(--color-surface-low)] p-7">
            <p className="text-xs uppercase tracking-[0.28em] text-[var(--color-text-soft)]">Dropzone</p>
            <div
              onDragOver={(event) => {
                event.preventDefault();
                setDropActive(true);
              }}
              onDragLeave={() => setDropActive(false)}
              onDrop={(event) => {
                event.preventDefault();
                setDropActive(false);
                void handleFilesSelected(event.dataTransfer.files);
              }}
              className={`mt-6 rounded-[32px] bg-[var(--color-surface-strong)] px-6 py-12 text-center shadow-[inset_0_0_0_1px_var(--color-outline)] ${dropActive ? "ring-2 ring-[var(--color-primary)]" : ""}`}
            >
              <div className="mx-auto flex h-16 w-16 items-center justify-center rounded-[22px] bg-[var(--color-surface-low)] text-[var(--color-primary)]">
                <CloudUpload size={28} />
              </div>
              <h2 className="font-heading mt-6 text-3xl font-semibold tracking-[-0.04em]">Drag and drop files</h2>
              <p className="mx-auto mt-4 max-w-md text-sm leading-7 text-[var(--color-text-muted)]">
                Files are uploaded directly to your Go backend-backed bucket using presigned MinIO URLs.
              </p>
              <p className="mt-3 text-sm text-[var(--color-text-soft)]">Destination: {uploadData.targetLabel}</p>

              <input
                ref={inputRef}
                type="file"
                multiple
                className="hidden"
                onChange={(event) => {
                  void handleFilesSelected(event.target.files);
                }}
              />

              <button
                type="button"
                onClick={() => inputRef.current?.click()}
                className="primary-gradient mt-8 rounded-full px-5 py-3 text-sm font-semibold text-white"
              >
                Browse Local Files
              </button>
            </div>
          </div>

          <div className="rounded-[36px] bg-[var(--color-surface-strong)] p-6 shadow-[0_12px_32px_rgba(26,28,25,0.06)]">
            <p className="text-xs uppercase tracking-[0.28em] text-[var(--color-text-soft)]">Queue Summary</p>
            <div className="mt-5 grid gap-4 sm:grid-cols-3">
              {summary.map((item) => (
                <div key={item.label} className="rounded-[24px] bg-[var(--color-surface-low)] px-4 py-5">
                  <p className="text-xs uppercase tracking-[0.24em] text-[var(--color-text-soft)]">{item.label}</p>
                  <p className="font-heading mt-4 text-3xl font-semibold tracking-[-0.04em]">{item.value}</p>
                </div>
              ))}
            </div>
            {statusMessage ? <p className="mt-4 text-sm text-[var(--color-primary)]">{statusMessage}</p> : null}
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
              <CircleButton icon={Pause} label="Pause All" disabled />
              <CircleButton icon={Play} label="Resume All" disabled />
            </div>
          </div>

          <div className="mt-6 grid gap-4">
            {transfers.length === 0 ? (
              <div className="rounded-[26px] bg-[var(--color-surface-low)] px-5 py-10 text-center text-sm text-[var(--color-text-soft)]">
                Choose files to start uploading into {uploadData.targetLabel}.
              </div>
            ) : null}

            {transfers.map((transfer) => {
              const percent = transfer.totalBytes > 0
                ? Math.min(100, Math.round((transfer.transferredBytes / transfer.totalBytes) * 100))
                : 0;

              return (
                <article
                  key={transfer.id}
                  className="rounded-[26px] bg-[var(--color-surface-low)] px-5 py-5 transition-transform duration-200 hover:-translate-y-0.5"
                >
                  <div className="flex items-start justify-between gap-4">
                    <div className="min-w-0">
                      <button
                        type="button"
                        onClick={() => setSelectedId(transfer.id)}
                        className="truncate text-left text-[15px] font-medium text-[var(--color-text)]"
                      >
                        {transfer.fileName}
                      </button>
                      <p className="mt-1 text-sm text-[var(--color-text-soft)]">{transfer.statusLabel}</p>
                    </div>
                    <button
                      type="button"
                      onClick={(event) => {
                        event.stopPropagation();
                        handleRemoveTransfer(transfer.id);
                      }}
                      aria-label={`Remove ${transfer.fileName}`}
                      className="rounded-full bg-[var(--color-surface-strong)] p-2 text-[var(--color-text-soft)]"
                    >
                      <X size={16} />
                    </button>
                  </div>

                  {transfer.status === "error" ? (
                    <div className="mt-4 flex items-start gap-2 rounded-[18px] bg-red-50 px-3 py-3 text-sm text-red-700">
                      <TriangleAlert size={16} className="mt-0.5 shrink-0" />
                      <span>{transfer.errorMessage ?? "Upload failed."}</span>
                    </div>
                  ) : (
                    <>
                      <div className="mt-5 h-2 rounded-full bg-white/70">
                        <div
                          className="primary-gradient h-2 rounded-full transition-[width] duration-300"
                          style={{ width: `${percent}%` }}
                        />
                      </div>

                      <div className="mt-4 flex items-center justify-between text-sm text-[var(--color-text-muted)]">
                        <span>{percent}%</span>
                        <span>
                          {formatBytes(transfer.transferredBytes)} of {formatBytes(transfer.totalBytes)}
                        </span>
                      </div>
                    </>
                  )}
                </article>
              );
            })}
          </div>
        </section>
      </div>
    </AppShell>
  );
}

function CircleButton({
  icon: Icon,
  label,
  disabled,
}: {
  icon: typeof Pause;
  label: string;
  disabled?: boolean;
}) {
  return (
    <button
      type="button"
      aria-label={label}
      disabled={disabled}
      className="flex h-11 w-11 items-center justify-center rounded-full bg-[var(--color-surface-low)] text-[var(--color-text-soft)] transition-colors hover:text-[var(--color-text)] disabled:cursor-not-allowed disabled:opacity-40"
    >
      <Icon size={16} />
    </button>
  );
}

function buildTransferId(file: File) {
  return `${file.name}-${file.size}-${crypto.randomUUID()}`;
}
