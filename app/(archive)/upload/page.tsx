import { UploadManagerView } from "@/components/upload/upload-manager-view";
import { getUploadScreenData } from "@/lib/services/transfer-service";

export default async function UploadPage() {
  const uploadData = await getUploadScreenData();
  return <UploadManagerView uploadData={uploadData} />;
}
