import { NextResponse } from "next/server";
import { auth } from "@/lib/auth";
import { listUsers } from "@/lib/keycloak-admin";

// GET /api/users → list all Keycloak users (admin only)
export async function GET() {
  const session = await auth();
  if (!session?.user?.realmRoles?.includes("platform-admin")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 });
  }

  try {
    const users = await listUsers();
    return NextResponse.json({ users });
  } catch (error) {
    return NextResponse.json(
      { error: "Failed to list users" },
      { status: 502 },
    );
  }
}
