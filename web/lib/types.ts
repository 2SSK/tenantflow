/** Shared types used across the dashboard */

export type Tenant = {
  tenantID: string;
  status: string;
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

export const AUDIT_EVENT_ICONS: Record<string, string> = {
  TENANT_CREATED: "\u{1f7e2}",
  TENANT_PROVISIONED: "\u{1f535}",
  TENANT_ACTIVATED: "\u2705",
  TENANT_FAILED: "\u{1f534}",
  TENANT_DELETING: "\u{1f7e0}",
  TENANT_DEPROVISIONED: "\u{1f535}",
  TENANT_DELETED: "\u26ab",
};
