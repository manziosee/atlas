"use client";

import { useEffect, type ReactNode } from "react";
import { usePathname, useRouter } from "next/navigation";
import { useConnection } from "@/lib/connection";
import { ClusterProvider } from "@/lib/cluster";
import { ConsoleLoading, ConsoleShell } from "@/components/console/shell";
import { ToastProvider } from "@/components/console/toast";

export default function ConsoleLayout({ children }: { children: ReactNode }) {
  const { conn, ready } = useConnection();
  const router = useRouter();
  const pathname = usePathname();

  useEffect(() => {
    if (ready && !conn) router.replace(`/connect/?next=${encodeURIComponent(pathname)}`);
  }, [ready, conn, router, pathname]);

  if (!ready || !conn) return <ConsoleLoading />;
  return (
    <ToastProvider>
      <ClusterProvider>
        <ConsoleShell>{children}</ConsoleShell>
      </ClusterProvider>
    </ToastProvider>
  );
}
