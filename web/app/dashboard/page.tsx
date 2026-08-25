"use client";

import { useSession } from "next-auth/react";

export default function DashboardPage() {
  const { data: session } = useSession();
  const user = session?.user;

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-bold text-zinc-900 dark:text-zinc-50">
          Welcome{user?.name ? `, ${user.name}` : ""}
        </h1>
        <p className="mt-1 text-sm text-zinc-600 dark:text-zinc-400">
          Here's what's happening with your tenants.
        </p>
      </div>

      {/* Quick info cards */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
        <div className="rounded-lg border border-zinc-200 bg-white p-6 dark:border-zinc-800 dark:bg-zinc-950">
          <p className="text-sm font-medium text-zinc-600 dark:text-zinc-400">
            Your Roles
          </p>
          <div className="mt-2 flex flex-wrap gap-1">
            {user?.realmRoles?.length ? (
              user.realmRoles.map((role) => (
                <span
                  key={role}
                  className="inline-flex items-center rounded-md bg-blue-50 px-2 py-1 text-xs font-medium text-blue-700 dark:bg-blue-900/20 dark:text-blue-400"
                >
                  {role}
                </span>
              ))
            ) : (
              <span className="text-xs text-zinc-400">No roles assigned</span>
            )}
          </div>
        </div>

        <div className="rounded-lg border border-zinc-200 bg-white p-6 dark:border-zinc-800 dark:bg-zinc-950">
          <p className="text-sm font-medium text-zinc-600 dark:text-zinc-400">
            User ID
          </p>
          <p className="mt-2 truncate font-mono text-sm text-zinc-900 dark:text-zinc-50">
            {user?.id ?? "N/A"}
          </p>
        </div>

        <div className="rounded-lg border border-zinc-200 bg-white p-6 dark:border-zinc-800 dark:bg-zinc-950">
          <p className="text-sm font-medium text-zinc-600 dark:text-zinc-400">
            Session Status
          </p>
          <p className="mt-2 flex items-center gap-2 text-sm text-green-600 dark:text-green-400">
            <span className="h-2 w-2 rounded-full bg-green-500" />
            Authenticated
          </p>
        </div>
      </div>
    </div>
  );
}
