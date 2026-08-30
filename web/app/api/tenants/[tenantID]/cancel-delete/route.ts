import { NextResponse } from "next/server";
import { auth } from "@/lib/auth";
import { apiFetch } from "@/lib/api";

// POST /api/tenants/:id/cancel-delete → cancel an in-flight soft-delete via Go API
export async function POST(
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
      `/api/v1/tenants/${tenantID}/cancel-delete`,
      session.user.accessToken,
      { method: "POST" },
    );
    return NextResponse.json({ ok: true });
  } catch {
    return NextResponse.json(
      { error: "Failed to cancel tenant deletion" },
      { status: 502 },
    );
  }
}