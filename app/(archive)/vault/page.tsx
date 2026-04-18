import { ArchiveSectionView } from "@/components/archive/archive-section-view";
import { getSectionContext } from "@/lib/services/archive-service";

export default async function VaultPage() {
  const context = await getSectionContext("vault");
  return <ArchiveSectionView context={context} />;
}
