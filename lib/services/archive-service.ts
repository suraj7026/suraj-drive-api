import type { ArchiveContext, SectionKey } from "@/lib/models/archive";
import { buckets, collections, files, transfers } from "@/lib/mock-data/archive";
import { delay } from "@/lib/utils/delay";
import { titleFromSegments } from "@/lib/utils/archive-path";

export async function getArchiveContext(bucketId: string, path: string[]): Promise<ArchiveContext | null> {
  await delay(40);

  const bucket = buckets.find((candidate) => candidate.id === bucketId);
  if (!bucket) {
    return null;
  }

  const pathKey = path.join("/");
  const hasPath = path.length === 0 || files.some((item) => item.bucketId === bucketId && item.kind === "folder" && item.path.join("/") === path.slice(0, -1).join("/") && item.slug === path.at(-1));

  if (!hasPath) {
    return null;
  }

  const items = files.filter((item) => item.bucketId === bucketId && item.path.join("/") === pathKey);
  const recents = files
    .filter((item) => item.bucketId === bucketId && item.kind === "file")
    .sort((a, b) => +new Date(b.updatedAt) - +new Date(a.updatedAt))
    .slice(0, 4);

  return {
    section: bucket.kind,
    eyebrow: path.length === 0 ? "The Archive" : `${bucket.name} / ${path.join(" / ")}`,
    heading: path.length === 0 ? bucket.name : titleFromSegments(path),
    bucket,
    path,
    currentFolderLabel: path.length === 0 ? bucket.name : path[path.length - 1],
    collections: bucketId === "my-archive" && path.length === 0 ? collections : [],
    recents,
    items,
    defaultSelectedId: items[0]?.id ?? null,
    transferQueue: transfers,
  };
}

export async function getSectionContext(section: Exclude<SectionKey, "archive">): Promise<ArchiveContext> {
  await delay(40);

  const bucket = buckets.find((candidate) => candidate.kind === section) ?? buckets[0];

  const sectionItems = files.filter((item) => {
    switch (section) {
      case "shared":
        return item.shared || item.bucketId === "shared";
      case "recents":
        return item.kind === "file";
      case "starred":
        return item.starred;
      case "vault":
        return item.vaulted || item.bucketId === "vault";
    }
  });

  const items = section === "recents"
    ? [...sectionItems].sort((a, b) => +new Date(b.updatedAt) - +new Date(a.updatedAt)).slice(0, 8)
    : sectionItems;

  return {
    section,
    eyebrow: bucket.description,
    heading:
      section === "shared"
        ? "Shared Rooms"
        : section === "recents"
          ? "Recent Artifacts"
          : section === "starred"
            ? "Starred Objects"
            : "Vault",
    bucket,
    path: [],
    currentFolderLabel: bucket.name,
    collections: [],
    recents: items.slice(0, 4),
    items,
    defaultSelectedId: items[0]?.id ?? null,
    transferQueue: transfers,
  };
}
