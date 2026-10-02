"use client";

import Link from "next/link";
import { useSession } from "next-auth/react";
import { useCallback, useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Loader2, RotateCcw, DatabaseZap } from "lucide-react";
import type { FailedRun } from "@/lib/types";

async function fetchFailedRuns(): Promise<FailedRun[]> {
  const res = await fetch("/api/failed-runs");
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  const data = (await res.json()) as { runs?: FailedRun[] };
  return data.runs ?? [];
}

// Dead letter queue: workflow runs that exhausted their retries and failed.
// Each row is an entry point back into the system — replay the workflow.
export default function FailedRunsPage() {
  const { data: session } = useSession();
  const [retrying, setRetrying] = useState<string | null>(null);
  const [runs, setRuns] = useState<FailedRun[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const isAdmin = session?.user?.realmRoles?.includes("platform-admin");

  const load = useCallback(async () => {
    try {
      const data = await fetchFailedRuns();
      setRuns(data);
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load failed runs");
    } finally {
      setLoading(false);
    }
  }, []);

  // Load once when the page opens. Retry calls load() to refresh.
  useEffect(() => {
    let ignore = false;
    (async () => {
      try {
        const data = await fetchFailedRuns();
        if (!ignore) {
          setRuns(data);
          setError(null);
        }
      } catch (err) {
        if (!ignore)
          setError(err instanceof Error ? err.message : "Failed to load failed runs");
      } finally {
        if (!ignore) setLoading(false);
      }
    })();
    return () => {
      ignore = true;
    };
  }, []);

  const handleRetry = async (run: FailedRun) => {
    setRetrying(run.tenantID);
    try {
      const res = await fetch(`/api/tenants/${run.tenantID}/retry`, {
        method: "POST",
      });
      if (!res.ok) {
        const body = await res.json();
        throw new Error(body.error || `HTTP ${res.status}`);
      }
      await load(); // the run leaves the DLQ, so refresh the list
    } catch (err) {
      alert(err instanceof Error ? err.message : "Failed to retry run");
    } finally {
      setRetrying(null);
    }
  };

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">Failed Runs</h1>
          <p className="text-sm text-muted-foreground">
            Dead letter queue — workflow runs that exhausted their retries.{" "}
            {isAdmin
              ? "Replay a run to recover the tenant."
              : "Contact a platform admin to replay a run."}
          </p>
        </div>
        {isAdmin && (
          <Button variant="outline" size="sm" onClick={() => void load()}>
            <RotateCcw className="mr-1.5 h-3.5 w-3.5" />
            Refresh
          </Button>
        )}
      </div>

      {error && (
        <p className="rounded-md bg-destructive/10 px-3 py-2 text-sm text-destructive">
          {error}
        </p>
      )}

      {loading ? (
        <div className="flex items-center gap-2 py-16 text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" /> Loading failed runs…
        </div>
      ) : runs.length === 0 ? (
        <div className="flex flex-col items-center gap-2 py-16 text-center">
          <DatabaseZap className="h-8 w-8 text-muted-foreground" />
          <p className="text-sm text-muted-foreground">
            No failed runs. The queue is empty — well done.
          </p>
        </div>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Tenant</TableHead>
              <TableHead>Workflow</TableHead>
              <TableHead>Run ID</TableHead>
              <TableHead>Error</TableHead>
              <TableHead>Started</TableHead>
              {isAdmin && <TableHead className="text-right">Action</TableHead>}
            </TableRow>
          </TableHeader>
          <TableBody>
            {runs.map((run) => (
              <TableRow key={run.runID}>
                <TableCell>
                  <Link
                    href={`/dashboard/tenants/${run.tenantID}`}
                    className="font-mono text-sm hover:underline"
                  >
                    {run.tenantID}
                  </Link>
                </TableCell>
                <TableCell className="font-mono text-xs">
                  {run.workflowType}
                </TableCell>
                <TableCell className="font-mono text-xs text-muted-foreground">
                  {run.runID.slice(0, 8)}…
                </TableCell>
                <TableCell className="max-w-xs truncate text-xs text-destructive">
                  <span title={run.errorMessage}>{run.errorMessage}</span>
                </TableCell>
                <TableCell className="text-xs text-muted-foreground">
                  {new Date(run.startedAt).toLocaleString()}
                </TableCell>
                {isAdmin && (
                  <TableCell className="text-right">
                    <Button
                      variant="outline"
                      size="sm"
                      disabled={retrying === run.tenantID}
                      onClick={() => handleRetry(run)}
                    >
                      {retrying === run.tenantID ? (
                        <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
                      ) : (
                        <RotateCcw className="mr-1.5 h-3.5 w-3.5" />
                      )}
                      Retry
                    </Button>
                  </TableCell>
                )}
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  );
}