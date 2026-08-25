import { NextResponse } from "next/server";
import { auth } from "@/lib/auth";
import { apiFetch } from "@/lib/api";

// GET /api/tenants → list all tenants from Go API
export async function GET() {
  const session = await auth();
  if (!session?.user?.accessToken) {
    return NextResponse.json({ error: "Unauthorized" }, { status: 401 });
  }

  try {
    const data = await apiFetch<{
      tenants: Array<{
        tenantID: string;
        status: string;
        workflowID?: string;
        createdAt: string;
        updatedAt: string;
      }>;
    }>("/api/v1/tenants", session.user.accessToken);
    return NextResponse.json(data);
  } catch (error) {
    return NextResponse.json(
      { error: "Failed to fetch tenants" },
      { status: 502 },
    );
  }
}

export async function POST(request: Request) {
  const session = await auth();
  if (!session?.user?.accessToken) {
    return NextResponse.json({ error: "Unauthorized" }, { status: 401 });
  }

  try {
    const body = await request.json();
    const data = await apiFetch<{
      tenantID: string;
      workflowID: string;
      status: string;
    }>("/api/v1/tenants", session.user.accessToken, {
      method: "POST",
      body: JSON.stringify(body),
    });
    return NextResponse.json(data, { status: 202 });
  } catch (error) {
    return NextResponse.json(
      { error: "Failed to create tenant" },
      { status: 502 },
    );
  }
}
