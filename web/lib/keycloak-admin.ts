/**
 * Server-side Keycloak admin API client.
 *
 * Uses the master realm admin-cli client with password grant
 * to get an admin token, then calls the admin REST API.
 * Only import this in server components / route handlers (never in client code).
 */

const KC_URL = process.env.KEYCLOAK_ADMIN_URL!;
const KC_REALM = process.env.KEYCLOAK_ADMIN_REALM!;
const KC_USER = process.env.KEYCLOAK_ADMIN_USER!;
const KC_PASS = process.env.KEYCLOAK_ADMIN_PASS!;

let cachedToken: string | null = null;
let tokenExpiresAt = 0;

async function getAdminToken(): Promise<string> {
  if (cachedToken && Date.now() < tokenExpiresAt) {
    return cachedToken;
  }

  const res = await fetch(
    `${KC_URL}/realms/master/protocol/openid-connect/token`,
    {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body: new URLSearchParams({
        grant_type: "password",
        client_id: "admin-cli",
        username: KC_USER,
        password: KC_PASS,
      }),
    },
  );

  if (!res.ok) {
    throw new Error(`Keycloak admin token failed: ${res.status}`);
  }

  const data = await res.json();
  cachedToken = data.access_token;
  // Refresh 30s before expiry
  tokenExpiresAt = Date.now() + (data.expires_in - 30) * 1000;
  return cachedToken!;
}

export interface KcUser {
  id: string;
  username: string;
  email?: string;
  firstName?: string;
  lastName?: string;
  enabled: boolean;
  realmRoles?: string[];
}

export interface KcRole {
  id: string;
  name: string;
  description?: string;
}

/**
 * GET /admin/realms/{realm}/users
 * Returns all users in the realm.
 */
export async function listUsers(): Promise<KcUser[]> {
  const token = await getAdminToken();
  const res = await fetch(`${KC_URL}/admin/realms/${KC_REALM}/users`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!res.ok) throw new Error(`list users ${res.status}`);
  const users = await res.json();

  // For each user, fetch their realm roles
  const usersWithRoles = await Promise.all(
    users.map(async (u: KcUser) => {
      try {
        const roleRes = await fetch(
          `${KC_URL}/admin/realms/${KC_REALM}/users/${u.id}/role-mappings/realm`,
          { headers: { Authorization: `Bearer ${token}` } },
        );
        if (roleRes.ok) {
          const roles: KcRole[] = await roleRes.json();
          u.realmRoles = roles.map((r) => r.name);
        }
      } catch {
        // Ignore role fetch errors
      }
      return u;
    }),
  );

  return usersWithRoles;
}

/**
 * GET /admin/realms/{realm}/roles
 * Returns all realm roles.
 */
export async function listRoles(): Promise<KcRole[]> {
  const token = await getAdminToken();
  const res = await fetch(`${KC_URL}/admin/realms/${KC_REALM}/roles`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!res.ok) throw new Error(`list roles ${res.status}`);
  return res.json();
}

/**
 * DELETE /admin/realms/{realm}/users/{id}
 */
export async function deleteUser(userID: string): Promise<void> {
  const token = await getAdminToken();
  const res = await fetch(
    `${KC_URL}/admin/realms/${KC_REALM}/users/${userID}`,
    {
      method: "DELETE",
      headers: { Authorization: `Bearer ${token}` },
    },
  );
  if (!res.ok && res.status !== 404) {
    throw new Error(`delete user ${res.status}`);
  }
}

/**
 * POST /admin/realms/{realm}/roles/{roleName}/users
 * Assign a realm role to a user.
 */
export async function assignRole(
  userID: string,
  roleName: string,
): Promise<void> {
  const token = await getAdminToken();

  // First get the role by name
  const roleRes = await fetch(
    `${KC_URL}/admin/realms/${KC_REALM}/roles/${roleName}`,
    { headers: { Authorization: `Bearer ${token}` } },
  );
  if (!roleRes.ok) throw new Error(`get role ${roleRes.status}`);
  const role: KcRole = await roleRes.json();

  // Then assign it
  const res = await fetch(
    `${KC_URL}/admin/realms/${KC_REALM}/users/${userID}/role-mappings/realm`,
    {
      method: "POST",
      headers: {
        Authorization: `Bearer ${token}`,
        "Content-Type": "application/json",
      },
      body: JSON.stringify([{ id: role.id, name: role.name }]),
    },
  );
  if (!res.ok && res.status !== 409) {
    throw new Error(`assign role ${res.status}`);
  }
}

/**
 * DELETE /admin/realms/{realm}/users/{userID}/role-mappings/realm
 * Remove a realm role from a user.
 */
export async function removeRole(
  userID: string,
  roleName: string,
): Promise<void> {
  const token = await getAdminToken();

  const roleRes = await fetch(
    `${KC_URL}/admin/realms/${KC_REALM}/roles/${roleName}`,
    { headers: { Authorization: `Bearer ${token}` } },
  );
  if (!roleRes.ok) throw new Error(`get role ${roleRes.status}`);
  const role: KcRole = await roleRes.json();

  const res = await fetch(
    `${KC_URL}/admin/realms/${KC_REALM}/users/${userID}/role-mappings/realm`,
    {
      method: "DELETE",
      headers: {
        Authorization: `Bearer ${token}`,
        "Content-Type": "application/json",
      },
      body: JSON.stringify([{ id: role.id, name: role.name }]),
    },
  );
  if (!res.ok && res.status !== 404) {
    throw new Error(`remove role ${res.status}`);
  }
}
