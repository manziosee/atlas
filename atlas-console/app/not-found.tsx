import Link from "next/link";
import { Logo } from "@/components/logo";

export default function NotFound() {
  return (
    <main id="main" className="grid min-h-dvh place-items-center px-4 text-center">
      <div>
        <Logo className="mx-auto h-12 w-12" />
        <h1 className="mt-6 text-3xl font-semibold tracking-tight">This page drifted out of orbit</h1>
        <p className="mt-2 text-muted">The page you asked for doesn&apos;t exist.</p>
        <div className="mt-6 flex justify-center gap-3">
          <Link href="/" className="btn btn-outline">
            Home
          </Link>
          <Link href="/dashboard/" className="btn btn-lime">
            Open the console
          </Link>
        </div>
      </div>
    </main>
  );
}
