"use client";

import { useSession } from "next-auth/react";
import { useEffect, useState } from "react";
import Link from "next/link";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Separator } from "@/components/ui/separator";
import { TENANT_STATUS_CONFIG, type Tenant } from "@/lib/types";
import { Plus, Loader2, Server } from "lucide-react";

export default function TenantsPage() {
  const { data: session } = useSession();
  const [tenants, setTenants] = useState<Tenant[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [newTenantID, setNewTenantID] = useState("");
  const [creating, setCreating] = useState(false);

  const isAdmin = session?.user?.realmRoles?.includes("platform-admin");

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

  // Group tenants by status
  const activeCount = tenants.filter((t) => t.status === "active").length;
  const provisioningCount = tenants.filter((t) => t.status === "provisioning").length;
  const failedCount = tenants.filter((t) => t.status === "failed").length;

  return (
    <div className="flex h-full flex-col gap-4">
      {/* ── Pinned header ── */}
      <div className="shrink-0 space-y-3">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-3">
            <div className="flex h-9 w-9 items-center justify-center rounded-lg bg-primary/10">
              <Server className="h-5 w-5 text-primary" />
            </div>
            <div>
              <h1 className="text-2xl font-bold tracking-tight">Tenants</h1>
              <p className="text-sm text-muted-foreground">
                Manage your SaaS tenants and their lifecycle.
              </p>
            </div>
          </div>

          {/* Quick stats */}
          {!loading && tenants.length > 0 && (
            <div className="hidden items-center gap-4 text-sm md:flex">
              <div className="flex items-center gap-1.5">
                <span className="h-2 w-2 rounded-full bg-emerald-500" />
                <span className="text-muted-foreground">{activeCount} active</span>
              </div>
              <div className="flex items-center gap-1.5">
                <span className="h-2 w-2 rounded-full bg-amber-500" />
                <span className="text-muted-foreground">{provisioningCount} provisioning</span>
              </div>
              {failedCount > 0 && (
                <div className="flex items-center gap-1.5">
                  <span className="h-2 w-2 rounded-full bg-destructive" />
                  <span className="text-muted-foreground">{failedCount} failed</span>
                </div>
              )}
            </div>
          )}
        </div>

        {/* Create form */}
        {isAdmin && (
          <Card>
            <CardContent className="p-3">
              <form onSubmit={handleCreate} className="flex items-center gap-2">
                <span className="shrink-0 text-sm text-muted-foreground">New tenant</span>
                <Input
                  value={newTenantID}
                  onChange={(e) => setNewTenantID(e.target.value)}
                  placeholder="e.g. acme-corp"
                  className="max-w-xs font-mono text-sm"
                />
                <Button
                  type="submit"
                  size="sm"
                  disabled={creating || !newTenantID.trim()}
                >
                  {creating ? (
                    <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
                  ) : (
                    <Plus className="mr-1.5 h-3.5 w-3.5" />
                  )}
                  Create
                </Button>
              </form>
            </CardContent>
          </Card>
        )}

        <Separator />
      </div>

      {/* Status / error */}
      {loading && (
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" />
          Loading tenants...
        </div>
      )}
      {error && (
        <p className="text-sm text-destructive">{error}</p>
      )}

      {/* ── Scrollable table ── */}
      {!loading && !error && (
        <div className="min-h-0 flex-1 overflow-hidden rounded-xl">
          <div className="h-full overflow-auto">
            <table className="w-full caption-bottom text-sm">
              <thead className="sticky top-0 z-10 border-b border-border bg-muted/50 backdrop-blur-sm">
                <tr className="[&_th:last-child]:pr-4">
                  <th className="h-10 px-4 text-left font-medium text-muted-foreground">Tenant ID</th>
                  <th className="h-10 px-4 text-left font-medium text-muted-foreground">Status</th>
                  <th className="h-10 px-4 text-left font-medium text-muted-foreground">Created</th>
                  <th className="h-10 px-4 text-left font-medium text-muted-foreground">Updated</th>
                </tr>
              </thead>
              <tbody className="[&_tr:last-child]:border-0">
                {tenants.length === 0 ? (
                  <tr>
                    <td colSpan={4} className="h-24 text-center text-muted-foreground">
                      No tenants yet. Create one above.
                    </td>
                  </tr>
                ) : (
                  tenants.map((t) => {
                    const config = TENANT_STATUS_CONFIG[t.status];
                    return (
                      <tr
                        key={t.tenantID}
                        className="border-b border-border transition-colors hover:bg-muted/30"
                      >
                        <td className="px-4 py-2.5">
                          <Link
                            href={`/dashboard/tenants/${t.tenantID}`}
                            className="font-mono text-sm font-medium text-primary transition-colors hover:underline"
                          >
                            {t.tenantID}
                          </Link>
                        </td>
                        <td className="px-4 py-2.5">
                          <Badge variant="outline" className={config?.className}>
                            {config?.label ?? t.status}
                          </Badge>
                        </td>
                        <td className="px-4 py-2.5 text-muted-foreground">
                          {new Date(t.createdAt).toLocaleString()}
                        </td>
                        <td className="px-4 py-2.5 text-muted-foreground">
                          {new Date(t.updatedAt).toLocaleString()}
                        </td>
                      </tr>
                    );
                  })
                )}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </div>
  );
}
