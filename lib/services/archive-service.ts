import type { ArchiveContext, Bucket, FileItem, FileType, SectionKey } from "@/lib/models/archive";
import type { BackendFileObject, BackendFolderEntry, BackendListResponse, BackendSearchResponse } from "@/lib/models/backend";
import { titleFromSegments } from "@/lib/utils/archive-path";
import { serverApiFetch } from "@/lib/api/server";
import { requireCurrentUser } from "@/lib/services/auth-service";

const archiveDescription = "Private objects stored in your personal bucket.";

export async function getArchiveContext(bucketId: string, path: string[]): Promise<ArchiveContext | null> {
  const user = await requireCurrentUser();
  const normalizedPath = path.filter(Boolean);

  if (bucketId !== user.bucket) {
    return null;
  }

  const prefix = joinPath(normalizedPath);
  const response = await serverApiFetch<BackendListResponse>("/api/files" + toQueryString({ prefix }));
  const items = mapListingItems(response, user.bucket, user.name, normalizedPath);

  return {
    user,
    section: "archive" as const,
    eyebrow: normalizedPath.length === 0 ? "The Archive" : `My Archive / ${normalizedPath.join(" / ")}`,
    heading: normalizedPath.length === 0 ? "My Archive" : titleFromSegments(normalizedPath),
    bucket: buildBucket(user.bucket, "archive", archiveDescription),
    path: normalizedPath,
    currentFolderLabel: normalizedPath.at(-1) ?? "My Archive",
    collections: [],
    recents: sortFilesByUpdatedAt(items).slice(0, 4),
    items,
    defaultSelectedId: items[0]?.id ?? null,
    transferQueue: [],
    emptyStateMessage: normalizedPath.length === 0
      ? "Upload a file or create a folder to start filling this bucket."
      : "This folder is empty.",
  };
}

export async function getSearchContext(query: string): Promise<ArchiveContext> {
  const user = await requireCurrentUser();
  const trimmedQuery = query.trim();

  if (!trimmedQuery) {
    return {
      user,
      section: "archive" as const,
      eyebrow: "Archive Search",
      heading: "Search the archive",
      bucket: buildBucket(user.bucket, "archive", archiveDescription),
      path: [],
      currentFolderLabel: "Search",
      collections: [],
      recents: [],
      items: [],
      defaultSelectedId: null,
      transferQueue: [],
      emptyStateMessage: "Enter a file name in the search bar to query your bucket.",
    };
  }

  const response = await serverApiFetch<BackendSearchResponse>("/api/search" + toQueryString({ q: trimmedQuery }));
  const items = response.results.map((file, index) => mapFile(file, user.bucket, user.name, index));

  return {
    user,
    section: "archive" as const,
    eyebrow: "Archive Search",
    heading: `Results for \"${trimmedQuery}\"`,
    bucket: buildBucket(user.bucket, "archive", archiveDescription),
    path: [],
    currentFolderLabel: "Search Results",
    collections: [],
    recents: sortFilesByUpdatedAt(items).slice(0, 4),
    items,
    defaultSelectedId: items[0]?.id ?? null,
    transferQueue: [],
    emptyStateMessage: `No files matched \"${trimmedQuery}\".`,
  };
}

export async function getSectionContext(section: Exclude<SectionKey, "archive">): Promise<ArchiveContext> {
  const user = await requireCurrentUser();

  return {
    user,
    section,
    eyebrow: "Backend Coverage",
    heading: sectionTitle(section),
    bucket: buildBucket(user.bucket, section, sectionDescription(section)),
    path: [],
    currentFolderLabel: sectionTitle(section),
    collections: [],
    recents: [],
    items: [],
    defaultSelectedId: null,
    transferQueue: [],
    emptyStateMessage:
      "The current Go backend exposes personal bucket listing, uploads, downloads, copies, deletes, and search. This view needs dedicated backend metadata before it can show real content.",
  };
}

function buildBucket(id: string, kind: SectionKey, description: string): Bucket {
  return {
    id,
    name: "My Archive",
    kind,
    description,
  };
}

function mapListingItems(response: BackendListResponse, bucketId: string, owner: string, path: string[]) {
  const folders = response.folders.map((folder, index) => mapFolder(folder, bucketId, owner, path, index));
  const files = response.files.map((file, index) => mapFile(file, bucketId, owner, index + folders.length));
  return [...folders, ...files];
}

function mapFolder(folder: BackendFolderEntry, bucketId: string, owner: string, path: string[], index: number): FileItem {
  return {
    id: `folder:${folder.prefix}:${index}`,
    bucketId,
    kind: "folder",
    fileType: "folder",
    name: folder.name,
    slug: folder.name,
    path,
    owner,
    tags: ["Folder"],
  };
}

function mapFile(file: BackendFileObject, bucketId: string, owner: string, index: number): FileItem {
  const segments = splitKey(file.key);

  return {
    id: `file:${file.key}:${index}`,
    bucketId,
    kind: "file",
    fileType: inferFileType(file.name, file.content_type),
    name: file.name,
    slug: segments.slug,
    path: segments.path,
    owner,
    updatedAt: file.last_modified,
    sizeBytes: file.size,
    tags: file.content_type ? [file.content_type] : undefined,
  };
}

function splitKey(key: string) {
  const segments = key.split("/").filter(Boolean);
  return {
    path: segments.slice(0, -1),
    slug: segments.at(-1) ?? key,
  };
}

function inferFileType(name: string, contentType: string): FileType {
  const lowerName = name.toLowerCase();
  if (contentType.startsWith("image/") || /\.(png|jpe?g|gif|webp|svg|heic)$/i.test(lowerName)) {
    return "image";
  }
  if (contentType.startsWith("video/") || /\.(mp4|mov|avi|mkv|webm)$/i.test(lowerName)) {
    return "video";
  }
  if (contentType === "application/pdf" || lowerName.endsWith(".pdf")) {
    return "pdf";
  }
  if (contentType.includes("csv") || lowerName.endsWith(".csv")) {
    return "csv";
  }
  if (/\.(raw|cr2|nef|arw|dng)$/i.test(lowerName)) {
    return "raw";
  }
  return "other";
}

function sectionTitle(section: Exclude<SectionKey, "archive">): string {
  switch (section) {
    case "shared":
      return "Shared";
    case "recents":
      return "Recents";
    case "starred":
      return "Starred";
    case "vault":
      return "Vault";
  }
}

function sectionDescription(section: Exclude<SectionKey, "archive">): string {
  switch (section) {
    case "shared":
      return "Collaboration spaces will appear here once the backend exposes shared metadata.";
    case "recents":
      return "Recent activity needs backend-wide file history before it can be rendered accurately.";
    case "starred":
      return "Starred items need persisted metadata before they can be shown here.";
    case "vault":
      return "Vault items need a dedicated protected bucket or metadata flag from the backend.";
  }
}

function sortFilesByUpdatedAt(items: FileItem[]) {
  return items
    .filter((item): item is FileItem & { updatedAt: string } => item.kind === "file" && Boolean(item.updatedAt))
    .sort((left, right) => +new Date(right.updatedAt) - +new Date(left.updatedAt));
}

function joinPath(path: string[]) {
  return path.join("/");
}

function toQueryString(query: Record<string, string>) {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (!value) {
      continue;
    }
    params.set(key, value);
  }

  const search = params.toString();
  return search ? `?${search}` : "";
}
