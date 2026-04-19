import { redirect } from "next/navigation";
import { requireCurrentUser } from "@/lib/services/auth-service";

export default async function ArchiveLandingPage() {
  const user = await requireCurrentUser();
  redirect(`/archive/${user.bucket}`);
}
