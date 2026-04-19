import { redirect } from "next/navigation";
import { getCurrentUser } from "@/lib/services/auth-service";

export default async function HomePage() {
  const user = await getCurrentUser();
  redirect(user ? `/archive/${user.bucket}` : "/login");
}
