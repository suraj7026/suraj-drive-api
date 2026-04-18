import { FolderOpen, History, Shield, Star, Upload, Users } from "lucide-react";

export const navItems = [
  {
    href: "/archive/my-archive",
    label: "My Archive",
    icon: FolderOpen,
    match: (pathname: string) => pathname.startsWith("/archive"),
  },
  {
    href: "/shared",
    label: "Shared",
    icon: Users,
    match: (pathname: string) => pathname.startsWith("/shared"),
  },
  {
    href: "/recents",
    label: "Recents",
    icon: History,
    match: (pathname: string) => pathname.startsWith("/recents"),
  },
  {
    href: "/starred",
    label: "Starred",
    icon: Star,
    match: (pathname: string) => pathname.startsWith("/starred"),
  },
  {
    href: "/vault",
    label: "Vault",
    icon: Shield,
    match: (pathname: string) => pathname.startsWith("/vault"),
  },
  {
    href: "/upload",
    label: "Upload",
    icon: Upload,
    badge: "3",
    match: (pathname: string) => pathname.startsWith("/upload"),
  },
];
