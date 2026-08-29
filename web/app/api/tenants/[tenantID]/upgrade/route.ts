import { NextResponse } from "next/server";
import { auth } from "@/lib/auth";
import { apiFetch, ApiFetchError } from "@/lib/api";

// POST /api/tenants/:id/upgrade → upgrade tenant via Go API
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
      `/api/v1/tenants/${tenantID}/upgrade`,
      session.user.accessToken,
      { method: "POST" },
    );
    return NextResponse.json({ ok: true });
  } catch (err) {
    // Forward the upstream status (e.g. 409 Conflict for an in-progress
    // upgrade) and a real error message instead of a generic 502.
    const status = err instanceof ApiFetchError ? err.status : 502;
    const message =
      err instanceof Error
        ? err.message
        : "Failed to upgrade tenant";
    return NextResponse.json({ error: message }, { status });
  }
}
