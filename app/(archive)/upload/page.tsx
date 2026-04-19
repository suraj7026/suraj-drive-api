import { UploadManagerView } from "@/components/upload/upload-manager-view";
import { getUploadScreenData } from "@/lib/services/transfer-service";

type UploadPageProps = {
  searchParams: Promise<{ prefix?: string }>;
};

export default async function UploadPage({ searchParams }: UploadPageProps) {
  const { prefix } = await searchParams;
  const uploadData = await getUploadScreenData(prefix);
  return <UploadManagerView uploadData={uploadData} />;
}
