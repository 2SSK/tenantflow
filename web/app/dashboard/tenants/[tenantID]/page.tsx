"use client";

import { useEffect, useState } from "react";
import { useParams } from "next/navigation";
import Link from "next/link";

type Tenant = {
  tenantID: string;
  status: string;
  workflowID?: string;
  createdAt: string;
  updatedAt: string;
};

type AuditEvent = {
  ID: number;
  TenantID: string;
  WorkflowID?: string;
  EventType: string;
  Actor: string;
  Payload: Record<string, unknown>;
  CreatedAt: string;
};

const statusColors: Record<string, string> = {
  active: "bg-green-100 text-green-800",
  provisioning: "bg-yellow-100 text-yellow-800",
  pending: "bg-gray-100 text-gray-800",
  failed: "bg-red-100 text-red-800",
  deleting: "bg-orange-100 text-orange-800",
  deleted: "bg-red-50 text-red-600",
};

const eventIcons: Record<string, string> = {
  TENANT_CREATED: "\u{1f7e2}",
  TENANT_PROVISIONED: "\u{1f535}",
  TENANT_ACTIVATED: "\u2705",
  TENANT_FAILED: "\u{1f534}",
  TENANT_DELETING: "\u{1f7e0}",
  TENANT_DEPROVISIONED: "\u{1f535}",
  TENANT_DELETED: "\u26ab",
};

export default function TenantDetailPage() {
  const { tenantID } = useParams<{ tenantID: string }>();
  const [tenant, setTenant] = useState<Tenant | null>(null);
  const [events, setEvents] = useState<AuditEvent[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

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

  if (loading) return <p className="text-gray-500">Loading...</p>;
  if (error) return <p className="text-red-600">Error: {error}</p>;
  if (!tenant) return <p className="text-gray-500">Tenant not found</p>;

  return (
    <div>
      <Link
        href="/dashboard/tenants"
        className="text-sm text-blue-600 hover:underline"
      >
        &larr; Back to tenants
      </Link>

      <div className="mt-4 mb-8">
        <div className="flex items-center gap-3">
          <h1 className="text-2xl font-bold font-mono">{tenant.tenantID}</h1>
          <span
            className={`inline-block px-2 py-0.5 rounded text-xs font-medium ${statusColors[tenant.status] ?? "bg-gray-100"}`}
          >
            {tenant.status}
          </span>
        </div>
        <div className="mt-2 grid grid-cols-3 gap-4 text-sm text-gray-500">
          <div>
            <span className="font-medium text-gray-700">Created:</span>{" "}
            {new Date(tenant.createdAt).toLocaleString()}
          </div>
          <div>
            <span className="font-medium text-gray-700">Updated:</span>{" "}
            {new Date(tenant.updatedAt).toLocaleString()}
          </div>
          {tenant.workflowID && (
            <div>
              <span className="font-medium text-gray-700">Workflow:</span>{" "}
              <span className="font-mono text-xs">{tenant.workflowID}</span>
            </div>
          )}
        </div>
      </div>

      <h2 className="text-lg font-semibold mb-4">Audit Events</h2>
      {events.length === 0 ? (
        <p className="text-gray-400">No events recorded yet.</p>
      ) : (
        <div className="relative border-l-2 border-gray-200 ml-3 space-y-4">
          {events.map((event) => (
            <div key={event.ID} className="relative pl-6">
              <div className="absolute -left-2.5 top-1 w-4 h-4 rounded-full bg-white border-2 border-gray-300 flex items-center justify-center text-[10px]">
                {eventIcons[event.EventType] ?? "\u2022"}
              </div>
              <div className="bg-white border rounded-lg p-3">
                <div className="flex items-center justify-between">
                  <span className="text-sm font-medium">
                    {event.EventType}
                  </span>
                  <span className="text-xs text-gray-400">
                    {new Date(event.CreatedAt).toLocaleString()}
                  </span>
                </div>
                <p className="text-xs text-gray-500 mt-1">
                  Actor:{" "}
                  <span className="font-mono">{event.Actor}</span>
                </p>
                {event.WorkflowID && (
                  <p className="text-xs text-gray-500">
                    Workflow:{" "}
                    <span className="font-mono">{event.WorkflowID}</span>
                  </p>
                )}
                {Object.keys(event.Payload).length > 0 && (
                  <pre className="mt-2 text-xs bg-gray-50 rounded p-2 overflow-x-auto">
                    {JSON.stringify(event.Payload, null, 2)}
                  </pre>
                )}
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
