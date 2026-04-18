import { ArchiveSectionView } from "@/components/archive/archive-section-view";
import { getSectionContext } from "@/lib/services/archive-service";

export default async function SharedPage() {
  const context = await getSectionContext("shared");
  return <ArchiveSectionView context={context} />;
}
