"use client";

import { useEffect, useState } from "react";
import { useParams, useRouter } from "next/navigation";
import { useSession } from "next-auth/react";
import Link from "next/link";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";
import {
  TENANT_STATUS_CONFIG,
  AUDIT_EVENT_ICONS,
  type Tenant,
  type AuditEvent,
} from "@/lib/types";
import { ArrowLeft, Loader2, Trash2 } from "lucide-react";

export default function TenantDetailPage() {
  const { tenantID } = useParams<{ tenantID: string }>();
  const router = useRouter();
  const { data: session } = useSession();
  const [tenant, setTenant] = useState<Tenant | null>(null);
  const [events, setEvents] = useState<AuditEvent[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);

  const isAdmin = session?.user?.realmRoles?.includes("platform-admin");

  useEffect(() => {
    const load = async () => {
      setLoading(true);
      try {
        const [tenantRes, eventsRes] = await Promise.all([
          fetch(`/api/tenants/${tenantID}`),
          fetch(`/api/tenants/${tenantID}/events`),
        ]);
        if (!tenantRes.ok) throw new Error("Tenant not found");
        setTenant(await tenantRes.json());
        if (eventsRes.ok) {
          const data = await eventsRes.json();
          setEvents(data.events ?? []);
        }
      } catch (err) {
        setError(err instanceof Error ? err.message : "Failed to load");
      } finally {
        setLoading(false);
      }
    };
    load();
  }, [tenantID]);

  const handleDelete = async () => {
    setDeleting(true);
    try {
      const res = await fetch(`/api/tenants/${tenantID}`, {
        method: "DELETE",
      });
      if (!res.ok) {
        const body = await res.json();
        throw new Error(body.error || `HTTP ${res.status}`);
      }
      router.push("/dashboard/tenants");
    } catch (err) {
      alert(err instanceof Error ? err.message : "Failed to delete tenant");
      setDeleting(false);
      setConfirmDelete(false);
    }
  };

  if (loading) {
    return (
      <div className="flex items-center gap-2 text-sm text-muted-foreground">
        <Loader2 className="h-4 w-4 animate-spin" />
        Loading...
      </div>
    );
  }

  if (error) return <p className="text-sm text-destructive">{error}</p>;
  if (!tenant) return <p className="text-sm text-muted-foreground">Tenant not found</p>;

  const statusConfig = TENANT_STATUS_CONFIG[tenant.status];

  return (
    <div className="flex h-full flex-col gap-6">
      {/* ── Pinned header ── */}
      <div className="shrink-0 space-y-4">
        <Link
          href="/dashboard/tenants"
          className="inline-flex items-center gap-2 rounded-lg px-3 py-1.5 text-sm font-medium text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
        >
          <ArrowLeft className="h-4 w-4" />
          Back to tenants
        </Link>

        <div className="flex items-start justify-between">
          <div>
            <div className="flex items-center gap-3">
              <h1 className="text-2xl font-bold font-mono tracking-tight">
                {tenant.tenantID}
              </h1>
              <Badge variant="outline" className={statusConfig?.className}>
                {statusConfig?.label ?? tenant.status}
              </Badge>
            </div>
            <div className="mt-3 grid gap-4 text-sm sm:grid-cols-3">
              <div>
                <p className="text-muted-foreground">Created</p>
                <p className="font-medium">
                  {new Date(tenant.createdAt).toLocaleString()}
                </p>
              </div>
              <div>
                <p className="text-muted-foreground">Updated</p>
                <p className="font-medium">
                  {new Date(tenant.updatedAt).toLocaleString()}
                </p>
              </div>
              {tenant.workflowID && (
                <div>
                  <p className="text-muted-foreground">Workflow</p>
                  <p className="font-mono text-xs">{tenant.workflowID}</p>
                </div>
              )}
            </div>
          </div>

          {/* Delete button — admin only */}
          {isAdmin && (
            <div className="shrink-0">
              {!confirmDelete ? (
                <Button
                  variant="destructive"
                  size="sm"
                  onClick={() => setConfirmDelete(true)}
                >
                  <Trash2 className="mr-1.5 h-3.5 w-3.5" />
                  Delete
                </Button>
              ) : (
                <div className="flex items-center gap-2">
                  <span className="text-sm text-muted-foreground">
                    Are you sure?
                  </span>
                  <Button
                    variant="destructive"
                    size="sm"
                    disabled={deleting}
                    onClick={handleDelete}
                  >
                    {deleting ? (
                      <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
                    ) : (
                      <Trash2 className="mr-1.5 h-3.5 w-3.5" />
                    )}
                    Yes, delete
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => setConfirmDelete(false)}
                  >
                    Cancel
                  </Button>
                </div>
              )}
            </div>
          )}
        </div>
      </div>

      <Separator />

      {/* ── Scrollable audit events ── */}
      <div className="min-h-0 flex-1">
        <h2 className="mb-4 text-lg font-semibold">Audit Events</h2>
        {events.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            No events recorded yet.
          </p>
        ) : (
          <div className="relative ml-3 space-y-4 border-l-2 border-border pl-6">
            {events.map((event) => (
              <div key={event.ID} className="relative">
                {/* Timeline dot */}
                <div className="absolute -left-[31px] top-1 flex h-4 w-4 items-center justify-center rounded-full border-2 border-border bg-background text-[10px]">
                  {AUDIT_EVENT_ICONS[event.EventType] ?? "\u2022"}
                </div>

                <Card>
                  <CardContent className="p-3">
                    <div className="flex items-center justify-between">
                      <span className="text-sm font-medium">
                        {event.EventType}
                      </span>
                      <span className="text-xs text-muted-foreground">
                        {new Date(event.CreatedAt).toLocaleString()}
                      </span>
                    </div>
                    <p className="mt-1 text-xs text-muted-foreground">
                      Actor:{" "}
                      <span className="font-mono">{event.Actor}</span>
                    </p>
                    {event.WorkflowID && (
                      <p className="text-xs text-muted-foreground">
                        Workflow:{" "}
                        <span className="font-mono">
                          {event.WorkflowID}
                        </span>
                      </p>
                    )}
                    {Object.keys(event.Payload).length > 0 && (
                      <pre className="mt-2 overflow-x-auto rounded-md bg-muted p-2 font-mono text-xs">
                        {JSON.stringify(event.Payload, null, 2)}
                      </pre>
                    )}
                  </CardContent>
                </Card>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
