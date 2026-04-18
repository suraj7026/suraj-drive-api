import { transfers } from "@/lib/mock-data/archive";
import type { UploadScreenData } from "@/lib/models/transfers";
import { delay } from "@/lib/utils/delay";

export async function getUploadScreenData(): Promise<UploadScreenData> {
  await delay(40);

  return {
    summary: [
      { label: "Queue Depth", value: "3 Items" },
      { label: "Destination", value: "My Archive" },
      { label: "Largest File", value: "1.2 GB" },
    ],
    transfers,
  };
}
