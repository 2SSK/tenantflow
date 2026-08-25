import { NextResponse } from "next/server";
import { auth } from "@/lib/auth";
import { deleteUser } from "@/lib/keycloak-admin";

// DELETE /api/users/:id → delete a Keycloak user (admin only)
export async function DELETE(
  _request: Request,
  { params }: { params: Promise<{ userID: string }> },
) {
  const { userID } = await params;
  const session = await auth();
  if (!session?.user?.realmRoles?.includes("platform-admin")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 });
  }

  try {
    await deleteUser(userID);
    return NextResponse.json({ success: true });
  } catch (error) {
    return NextResponse.json(
      { error: "Failed to delete user" },
      { status: 502 },
    );
  }
}
