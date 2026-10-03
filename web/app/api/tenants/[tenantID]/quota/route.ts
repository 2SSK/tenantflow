import { NextResponse } from "next/server";
import { auth } from "@/lib/auth";
import { apiFetch } from "@/lib/api";

// GET /api/tenants/:id/quota → tenant's plan limits from the Go API.
// Unlike /upgrade (which starts a workflow), this is a plain read: the quota
// is already persisted by the workflow, so a reload after upgrading shows
// the raised values.
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
      tenantID: string;
      maxUsers: number;
      maxStorageGB: number;
      maxSeats: number;
    }>(`/api/v1/tenants/${tenantID}/quota`, session.user.accessToken);
    return NextResponse.json(data);
  } catch {
    return NextResponse.json(
      { error: "Failed to load quota" },
      { status: 502 },
    );
  }
}