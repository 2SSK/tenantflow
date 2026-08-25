"use client";

import { useSession } from "next-auth/react";
import { useEffect, useState } from "react";
import Link from "next/link";

type Tenant = {
  tenantID: string;
  status: string;
  workflowID?: string;
  createdAt: string;
  updatedAt: string;
};

const statusColors: Record<string, string> = {
  active: "bg-green-100 text-green-800",
  provisioning: "bg-yellow-100 text-yellow-800",
  pending: "bg-gray-100 text-gray-800",
  failed: "bg-red-100 text-red-800",
  deleting: "bg-orange-100 text-orange-800",
  deleted: "bg-red-50 text-red-600",
};

export default function TenantsPage() {
  const { data: session } = useSession();
  const [tenants, setTenants] = useState<Tenant[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [newTenantID, setNewTenantID] = useState("");
  const [creating, setCreating] = useState(false);

  const fetchTenants = async () => {
    setLoading(true);
    setError(null);
    try {
      const res = await fetch("/api/tenants");
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const data = await res.json();
      setTenants(data.tenants ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load tenants");
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    fetchTenants();
  }, []);

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!newTenantID.trim()) return;
    setCreating(true);
    try {
      const res = await fetch("/api/tenants", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ tenantID: newTenantID.trim() }),
      });
      if (!res.ok) {
        const body = await res.json();
        throw new Error(body.error || `HTTP ${res.status}`);
      }
      setNewTenantID("");
      fetchTenants();
    } catch (err) {
      alert(err instanceof Error ? err.message : "Failed to create tenant");
    } finally {
      setCreating(false);
    }
  };

  const isAdmin = session?.user?.realmRoles?.includes("platform-admin");

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <h1 className="text-2xl font-bold">Tenants</h1>
      </div>

      {isAdmin && (
        <form onSubmit={handleCreate} className="mb-6 flex gap-2">
          <input
            type="text"
            value={newTenantID}
            onChange={(e) => setNewTenantID(e.target.value)}
            placeholder="Tenant ID (e.g. acme-corp)"
            className="border rounded px-3 py-2 text-sm flex-1 max-w-sm"
          />
          <button
            type="submit"
            disabled={creating || !newTenantID.trim()}
            className="bg-blue-600 text-white px-4 py-2 rounded text-sm disabled:opacity-50"
          >
            {creating ? "Creating..." : "Create Tenant"}
          </button>
        </form>
      )}

      {loading && <p className="text-gray-500">Loading tenants...</p>}
      {error && <p className="text-red-600">Error: {error}</p>}

      {!loading && !error && (
        <div className="border rounded-lg overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-gray-50 border-b">
              <tr>
                <th className="text-left px-4 py-3 font-medium">Tenant ID</th>
                <th className="text-left px-4 py-3 font-medium">Status</th>
                <th className="text-left px-4 py-3 font-medium">Created</th>
                <th className="text-left px-4 py-3 font-medium">Updated</th>
              </tr>
            </thead>
            <tbody className="divide-y">
              {tenants.length === 0 && (
                <tr>
                  <td colSpan={4} className="px-4 py-8 text-center text-gray-400">
                    No tenants yet. Create one above.
                  </td>
                </tr>
              )}
              {tenants.map((t) => (
                <tr key={t.tenantID} className="hover:bg-gray-50">
                  <td className="px-4 py-3 font-mono text-xs">
                    <Link href={`/dashboard/tenants/${t.tenantID}`} className="text-blue-600 hover:underline">
                      {t.tenantID}
                    </Link>
                  </td>
                  <td className="px-4 py-3">
                    <span className={`inline-block px-2 py-0.5 rounded text-xs font-medium ${statusColors[t.status] ?? "bg-gray-100"}`}>
                      {t.status}
                    </span>
                  </td>
                  <td className="px-4 py-3 text-gray-500">
                    {new Date(t.createdAt).toLocaleString()}
                  </td>
                  <td className="px-4 py-3 text-gray-500">
                    {new Date(t.updatedAt).toLocaleString()}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
