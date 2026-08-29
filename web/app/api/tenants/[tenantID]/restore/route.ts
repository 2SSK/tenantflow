import { NextResponse } from "next/server";
import { auth } from "@/lib/auth";
import { apiFetch, ApiFetchError } from "@/lib/api";

// POST /api/tenants/:id/restore → restore a specific backup via Go API
export async function POST(
  request: Request,
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

  let backupID: number;
  try {
    const body = await request.json();
    backupID = Number(body.backupID);
  } catch {
    return NextResponse.json(
      { error: "backupID is required" },
      { status: 400 },
    );
  }
  if (!Number.isInteger(backupID) || backupID <= 0) {
    return NextResponse.json(
      { error: "backupID is required" },
      { status: 400 },
    );
  }

  try {
    await apiFetch<unknown>(
      `/api/v1/tenants/${tenantID}/restore`,
      session.user.accessToken,
      { method: "POST", body: JSON.stringify({ backupID }) },
    );
    return NextResponse.json({ ok: true });
  } catch (err) {
    const status = err instanceof ApiFetchError ? err.status : 502;
    const message =
      err instanceof Error ? err.message : "Failed to restore tenant";
    return NextResponse.json({ error: message }, { status });
  }
}
