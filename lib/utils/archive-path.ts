export function buildArchiveHref(bucketId: string, segments: string[]) {
  if (segments.length === 0) {
    return `/archive/${bucketId}`;
  }

  return `/archive/${bucketId}/${segments.map(encodeURIComponent).join("/")}`;
}

export function titleFromSegments(segments: string[]) {
  return segments[segments.length - 1] ?? "Archive";
}
