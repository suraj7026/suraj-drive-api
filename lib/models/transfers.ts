export type TransferStatus = "queued" | "uploading" | "paused" | "done";

export type TransferItem = {
  id: string;
  fileName: string;
  totalBytes: number;
  transferredBytes: number;
  tickBytes: number;
  status: TransferStatus;
  statusLabel: string;
  targetLabel: string;
};

export type UploadScreenData = {
  summary: Array<{ label: string; value: string }>;
  transfers: TransferItem[];
};
