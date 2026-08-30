import { NextResponse } from "next/server";
import { auth } from "@/lib/auth";
import { apiFetch } from "@/lib/api";
import type { ListFailedRunsResponse } from "@/lib/types";

// GET /api/failed-runs → dead letter queue (failed workflow runs) via Go API
export async function GET() {
  const session = await auth();
  if (!session?.user?.accessToken) {
    return NextResponse.json({ error: "Unauthorized" }, { status: 401 });
  }
  if (!session?.user?.realmRoles?.includes("platform-admin")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 });
  }

  try {
    const data = await apiFetch<ListFailedRunsResponse>(
      "/api/v1/failed-runs",
      session.user.accessToken,
    );
    return NextResponse.json(data);
  } catch {
    return NextResponse.json(
      { error: "Failed to load failed runs" },
      { status: 502 },
    );
  }
}