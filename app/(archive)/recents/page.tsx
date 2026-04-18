import { ArchiveSectionView } from "@/components/archive/archive-section-view";
import { getSectionContext } from "@/lib/services/archive-service";

export default async function RecentsPage() {
  const context = await getSectionContext("recents");
  return <ArchiveSectionView context={context} />;
}
