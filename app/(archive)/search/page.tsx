import { ArchiveSectionView } from "@/components/archive/archive-section-view";
import { getSearchContext } from "@/lib/services/archive-service";

type SearchPageProps = {
  searchParams: Promise<{ q?: string }>;
};

export default async function SearchPage({ searchParams }: SearchPageProps) {
  const { q } = await searchParams;
  const context = await getSearchContext(q ?? "");
  return <ArchiveSectionView context={context} />;
}
