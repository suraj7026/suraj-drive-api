import type { Metadata } from "next";
import { Google_Sans } from "next/font/google";
import "./globals.css";

const headingFont = Google_Sans({
  variable: "--font-heading",
  subsets: ["latin"],
  weight: ["400", "500", "600", "700"],
});

const bodyFont = Google_Sans({
  variable: "--font-body",
  subsets: ["latin"],
  weight: ["400", "500", "600", "700"],
});

export const metadata: Metadata = {
  title: "SDrive",
  description: "An editorial personal archive frontend connected to a Go and MinIO backend.",
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="en" className={`${headingFont.variable} ${bodyFont.variable} h-full`}>
      <body className="min-h-full bg-[var(--color-surface)] text-[var(--color-text)] antialiased">
        {children}
      </body>
    </html>
  );
}
