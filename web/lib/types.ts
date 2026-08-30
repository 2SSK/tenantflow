/** Shared types used across the dashboard */

import type { LucideIcon } from "lucide-react";
import {
  CircleDot,
  Database,
  CheckCircle2,
  ServerCrash,
  RotateCcw,
  CircleCheck,
  Trash2,
  TrendingUp,
  Rocket,
  CircleAlert,
  Undo2,
  RefreshCw,
  GitMerge,
  DatabaseZap,
} from "lucide-react";

export type Tenant = {
  tenantID: string;
  status: string;
  isolationMode: string;
  workflowID?: string;
  createdAt: string;
  updatedAt: string;
};

export type AuditEvent = {
  ID: number;
  TenantID: string;
  WorkflowID?: string;
  EventType: string;
  Actor: string;
  Payload: Record<string, unknown>;
  CreatedAt: string;
};

// A failed workflow-instance mirror held in the dead letter queue.
export type FailedRun = {
  tenantID: string;
  workflowType: string;
  workflowID: string;
  runID: string;
  errorMessage?: string;
  startedAt: string;
  finishedAt?: string;
};

export type ListFailedRunsResponse = {
  runs: FailedRun[];
};

export type KcUser = {
  id: string;
  username: string;
  email?: string;
  firstName?: string;
  lastName?: string;
  enabled: boolean;
  realmRoles?: string[];
};

export const TENANT_STATUS_CONFIG: Record<
  string,
  { label: string; className: string }
> = {
  active: {
    label: "Active",
    className: "bg-emerald-500/10 text-emerald-500 border-emerald-500/20",
  },
  provisioning: {
    label: "Provisioning",
    className: "bg-amber-500/10 text-amber-500 border-amber-500/20",
  },
  pending: {
    label: "Pending",
    className: "bg-muted text-muted-foreground border-border",
  },
  failed: {
    label: "Failed",
    className: "bg-destructive/10 text-destructive border-destructive/20",
  },
  deleting: {
    label: "Deleting",
    className: "bg-orange-500/10 text-orange-500 border-orange-500/20",
  },
  deleted: {
    label: "Deleted",
    className: "bg-muted text-muted-foreground border-border",
  },
};

// Icon shown on the audit timeline dot, keyed by event type.
export const AUDIT_EVENT_ICONS: Record<string, LucideIcon> = {
  TENANT_CREATED: CircleDot,
  TENANT_PROVISIONED: Database,
  TENANT_ACTIVATED: CheckCircle2,
  TENANT_FAILED: ServerCrash,
  TENANT_DELETING: RotateCcw,
  TENANT_DEPROVISIONED: CircleCheck,
  TENANT_DELETED: Trash2,
  TENANT_UPGRADING: TrendingUp,
  TENANT_UPGRADED: Rocket,
  TENANT_UPGRADE_FAILED: CircleAlert,
  TENANT_QUOTA_ROLLED_BACK: Undo2,
  TENANT_MIGRATING: RefreshCw,
  TENANT_MIGRATED: GitMerge,
  TENANT_MIGRATE_FAILED: CircleAlert,
  TENANT_BACKING_UP: RotateCcw,
  TENANT_BACKUP_CREATED: Database,
  TENANT_BACKUP_FAILED: CircleAlert,
  TENANT_RESTORING: RotateCcw,
  TENANT_RESTORED: DatabaseZap,
  TENANT_RESTORE_FAILED: CircleAlert,
  TENANT_DELETE_CANCELLED: Undo2,
  TENANT_DELETE_FAILED: CircleAlert,
  TENANT_REPROVISION_REQUESTED: RefreshCw,
  TENANT_PROVISION_ROLLED_BACK: Undo2,
  TENANT_MIGRATION_ROLLED_BACK: RotateCcw,
  TENANT_RESTORE_ROLLED_BACK: RotateCcw,
};

// Event types emitted by saga compensation (rollback) steps. Combined with
// the payload "compensation": true marker, this is what the compensation
// history view uses to classify rollbacks.
export const COMPENSATION_EVENT_TYPES = new Set([
  "TENANT_PROVISION_ROLLED_BACK",
  "TENANT_QUOTA_ROLLED_BACK",
  "TENANT_MIGRATION_ROLLED_BACK",
  "TENANT_RESTORE_ROLLED_BACK",
]);

export function isCompensationEvent(event: {
  EventType: string;
  Payload?: Record<string, unknown>;
}): boolean {
  return (
    COMPENSATION_EVENT_TYPES.has(event.EventType) ||
    event.Payload?.compensation === true
  );
}
