import { NextResponse } from "next/server";
import { auth } from "@/lib/auth";
import { assignRole, removeRole, listRoles } from "@/lib/keycloak-admin";

// GET /api/users/:id/roles → list user's realm roles
export async function GET(
  _request: Request,
  { params }: { params: Promise<{ userID: string }> },
) {
  const { userID } = await params;
  const session = await auth();
  if (!session?.user?.realmRoles?.includes("platform-admin")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 });
  }

  try {
    // We list all roles and return them — individual user role mapping
    // would need an extra endpoint, so we return all roles for now
    const roles = await listRoles();
    return NextResponse.json({ roles });
  } catch {
    return NextResponse.json(
      { error: "Failed to list roles" },
      { status: 502 },
    );
  }
}

// POST /api/users/:id/roles → assign a role to a user
export async function POST(
  request: Request,
  { params }: { params: Promise<{ userID: string }> },
) {
  const { userID } = await params;
  const session = await auth();
  if (!session?.user?.realmRoles?.includes("platform-admin")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 });
  }

  try {
    const { roleName } = await request.json();
    if (!roleName) {
      return NextResponse.json(
        { error: "roleName is required" },
        { status: 400 },
      );
    }
    await assignRole(userID, roleName);
    return NextResponse.json({ success: true });
  } catch {
    return NextResponse.json(
      { error: "Failed to assign role" },
      { status: 502 },
    );
  }
}

// DELETE /api/users/:id/roles → remove a role from a user
export async function DELETE(
  request: Request,
  { params }: { params: Promise<{ userID: string }> },
) {
  const { userID } = await params;
  const session = await auth();
  if (!session?.user?.realmRoles?.includes("platform-admin")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 });
  }

  try {
    const { roleName } = await request.json();
    if (!roleName) {
      return NextResponse.json(
        { error: "roleName is required" },
        { status: 400 },
      );
    }
    await removeRole(userID, roleName);
    return NextResponse.json({ success: true });
  } catch {
    return NextResponse.json(
      { error: "Failed to remove role" },
      { status: 502 },
    );
  }
}
