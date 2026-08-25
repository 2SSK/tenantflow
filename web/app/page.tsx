"use client";

import { signIn } from "next-auth/react";

export default function Home() {
  return (
    <div className="flex flex-1 items-center justify-center bg-zinc-50 dark:bg-black">
      <main className="flex flex-col items-center gap-8 px-8">
        <h1 className="text-4xl font-bold tracking-tight text-zinc-900 dark:text-zinc-50">
          TenantFlow
        </h1>
        <p className="text-lg text-zinc-600 dark:text-zinc-400">
          Tenant management dashboard
        </p>
        <button
          onClick={() => signIn("keycloak")}
          className="rounded-lg bg-blue-600 px-6 py-3 text-sm font-semibold text-white shadow-sm hover:bg-blue-500 transition-colors"
        >
          Sign in with Keycloak
        </button>
      </main>
    </div>
  );
}
