import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "4DSky MLAT",
  description: "Live decentralized MLAT dashboard for Neuron / 4DSky",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
