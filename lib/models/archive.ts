import type { CurrentUser } from "@/lib/models/auth";
import type { TransferItem } from "@/lib/models/transfers";

export type SectionKey = "archive" | "shared" | "recents" | "starred" | "vault";

export type Bucket = {
  id: string;
  name: string;
  kind: SectionKey;
  description: string;
};

export type CollectionCard = {
  id: string;
  title: string;
  objectCount: number;
  updatedLabel: string;
  shared?: boolean;
  href: string;
};

export type FileType = "folder" | "image" | "raw" | "video" | "pdf" | "csv" | "other";

export type FileItem = {
  id: string;
  bucketId: string;
  kind: "file" | "folder";
  fileType?: FileType;
  name: string;
  slug: string;
  path: string[];
  owner: string;
  updatedAt?: string;
  sizeBytes?: number;
  tags?: string[];
  shared?: boolean;
  starred?: boolean;
  vaulted?: boolean;
};

export type ArchiveContext = {
  user: CurrentUser;
  section: SectionKey;
  eyebrow: string;
  heading: string;
  bucket: Bucket;
  path: string[];
  currentFolderLabel: string;
  collections: CollectionCard[];
  recents: FileItem[];
  items: FileItem[];
  defaultSelectedId: string | null;
  transferQueue: TransferItem[];
  emptyStateMessage?: string;
};
