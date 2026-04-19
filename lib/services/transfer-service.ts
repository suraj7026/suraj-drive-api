import { formatBytes } from "@/lib/utils/format";
import type { UploadScreenData } from "@/lib/models/transfers";
import { requireCurrentUser } from "@/lib/services/auth-service";

export async function getUploadScreenData(prefix = ""): Promise<UploadScreenData> {
  const user = await requireCurrentUser();
  const targetPrefix = prefix.trim().replace(/^\/+|\/+$/g, "");
  const targetLabel = targetPrefix ? `My Archive / ${targetPrefix}` : "My Archive";

  return {
    user,
    targetLabel,
    targetPrefix,
    summary: [
      { label: "Queue Depth", value: "0 Items" },
      { label: "Destination", value: targetLabel },
      { label: "Largest File", value: formatBytes(0) },
    ],
    transfers: [],
  };
}
