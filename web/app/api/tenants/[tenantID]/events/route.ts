import { NextResponse } from "next/server";
import { auth } from "@/lib/auth";
import { apiFetch } from "@/lib/api";

// GET /api/tenants/:id/events → list audit events from Go API
export async function GET(
  _request: Request,
  { params }: { params: Promise<{ tenantID: string }> },
) {
  const { tenantID } = await params;
  const session = await auth();
  if (!session?.user?.accessToken) {
    return NextResponse.json({ error: "Unauthorized" }, { status: 401 });
  }

  try {
    const data = await apiFetch<{
      events: Array<{
        ID: number;
        TenantID: string;
        WorkflowID?: string;
        EventType: string;
        Actor: string;
        Payload: Record<string, unknown>;
        CreatedAt: string;
      }>;
    }>(`/api/v1/tenants/${tenantID}/events`, session.user.accessToken);
    return NextResponse.json(data);
  } catch {
    return NextResponse.json(
      { error: "Failed to fetch events" },
      { status: 502 },
    );
  }
}
