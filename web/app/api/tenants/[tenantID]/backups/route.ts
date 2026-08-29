import { NextResponse } from "next/server";
import { auth } from "@/lib/auth";
import { apiFetch } from "@/lib/api";

// Backup mirrors the Go API's BackupResponse shape.
type Backup = {
  id: number;
  tenantID: string;
  filename: string;
  status: string;
  createdAt: string;
  completedAt?: string;
};

// GET /api/tenants/:id/backups → list backup history from Go API
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
    const data = await apiFetch<{ backups: Backup[] }>(
      `/api/v1/tenants/${tenantID}/backups`,
      session.user.accessToken,
    );
    return NextResponse.json(data);
  } catch {
    return NextResponse.json(
      { error: "Failed to fetch backups" },
      { status: 502 },
    );
  }
}
