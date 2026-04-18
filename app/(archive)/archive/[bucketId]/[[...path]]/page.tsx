import { notFound } from "next/navigation";
import { ArchiveBrowserView } from "@/components/archive/archive-browser-view";
import { getArchiveContext } from "@/lib/services/archive-service";

type PageProps = {
  params: Promise<{ bucketId: string; path?: string[] }>;
};

export default async function ArchivePathPage({ params }: PageProps) {
  const { bucketId, path } = await params;
  const context = await getArchiveContext(bucketId, path ?? []);

  if (!context) {
    notFound();
  }

  return <ArchiveBrowserView context={context} />;
}
