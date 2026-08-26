import { NextResponse } from "next/server";
import { auth } from "@/lib/auth";
import { apiFetch } from "@/lib/api";

// GET /api/tenants/:id → get tenant from Go API
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
      status: string;
      workflowID?: string;
      createdAt: string;
      updatedAt: string;
    }>(`/api/v1/tenants/${tenantID}`, session.user.accessToken);
    return NextResponse.json(data);
  } catch {
    return NextResponse.json({ error: "Tenant not found" }, { status: 404 });
  }
}

// DELETE /api/tenants/:id → delete tenant via Go API
export async function DELETE(
  _request: Request,
  { params }: { params: Promise<{ tenantID: string }> },
) {
  const { tenantID } = await params;
  const session = await auth();
  if (!session?.user?.accessToken) {
    return NextResponse.json({ error: "Unauthorized" }, { status: 401 });
  }
  if (!session?.user?.realmRoles?.includes("platform-admin")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 });
  }

  try {
    await apiFetch<unknown>(
      `/api/v1/tenants/${tenantID}`,
      session.user.accessToken,
      { method: "DELETE" },
    );
    return NextResponse.json({ ok: true });
  } catch {
    return NextResponse.json(
      { error: "Failed to delete tenant" },
      { status: 502 },
    );
  }
}
