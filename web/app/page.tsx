"use client";

import { signIn, useSession } from "next-auth/react";
import { useRouter } from "next/navigation";
import { useEffect } from "react";

export default function Home() {
  const { data: session } = useSession();
  const router = useRouter();

  // Redirect to dashboard if already logged in
  useEffect(() => {
    if (session) {
      router.replace("/dashboard");
    }
  }, [session, router]);

  return (
    <div className="flex flex-1 items-center justify-center">
      <main className="flex flex-col items-center gap-6 px-8">
        <div className="flex h-16 w-16 items-center justify-center rounded-2xl bg-primary text-2xl font-bold text-primary-foreground">
          T
        </div>
        <h1 className="text-3xl font-bold tracking-tight text-foreground">
          TenantFlow
        </h1>
        <p className="text-muted-foreground">
          Multi-tenant SaaS control plane
        </p>
        <button
          onClick={() => signIn("keycloak", { callbackUrl: "/dashboard" })}
          className="rounded-lg bg-primary px-6 py-3 text-sm font-semibold text-primary-foreground shadow-sm transition-colors hover:bg-primary/90"
        >
          Sign in with Keycloak
        </button>
      </main>
    </div>
  );
}
