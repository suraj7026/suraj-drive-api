import type { CurrentUser } from "@/lib/models/auth";

export type TransferStatus = "queued" | "uploading" | "paused" | "done" | "error";

export type TransferItem = {
  id: string;
  fileName: string;
  totalBytes: number;
  transferredBytes: number;
  tickBytes?: number;
  status: TransferStatus;
  statusLabel: string;
  targetLabel: string;
  objectKey?: string;
  errorMessage?: string;
};

export type UploadScreenData = {
  user: CurrentUser;
  targetLabel: string;
  targetPrefix: string;
  summary: Array<{ label: string; value: string }>;
  transfers: TransferItem[];
};
