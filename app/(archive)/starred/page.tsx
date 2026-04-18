import { ArchiveSectionView } from "@/components/archive/archive-section-view";
import { getSectionContext } from "@/lib/services/archive-service";

export default async function StarredPage() {
  const context = await getSectionContext("starred");
  return <ArchiveSectionView context={context} />;
}
