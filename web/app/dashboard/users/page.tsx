"use client";

import { useSession } from "next-auth/react";
import { Fragment, useEffect, useState } from "react";

type KcUser = {
  id: string;
  username: string;
  email?: string;
  firstName?: string;
  lastName?: string;
  enabled: boolean;
  realmRoles?: string[];
};

const roleBadgeColors: Record<string, string> = {
  "platform-admin": "bg-purple-100 text-purple-800",
  "platform-operator": "bg-blue-100 text-blue-800",
  "default-roles-tenantflow": "bg-gray-100 text-gray-600",
  offline_access: "bg-gray-100 text-gray-600",
  uma_authorization: "bg-gray-100 text-gray-600",
};

export default function UsersPage() {
  const { data: session } = useSession();
  const [users, setUsers] = useState<KcUser[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [expandedUser, setExpandedUser] = useState<string | null>(null);

  const isAdmin = session?.user?.realmRoles?.includes("platform-admin");

  const fetchUsers = async () => {
    setLoading(true);
    setError(null);
    try {
      const res = await fetch("/api/users");
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const data = await res.json();
      setUsers(data.users ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load users");
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    if (isAdmin) fetchUsers();
  }, [isAdmin]);

  const handleDelete = async (userID: string, username: string) => {
    if (!confirm(`Delete user "${username}"? This cannot be undone.`)) return;
    try {
      const res = await fetch(`/api/users/${userID}`, { method: "DELETE" });
      if (!res.ok) {
        const body = await res.json();
        throw new Error(body.error || `HTTP ${res.status}`);
      }
      fetchUsers();
    } catch (err) {
      alert(err instanceof Error ? err.message : "Failed to delete user");
    }
  };

  const handleToggleRole = async (
    userID: string,
    roleName: string,
    hasRole: boolean,
  ) => {
    try {
      const method = hasRole ? "DELETE" : "POST";
      const res = await fetch(`/api/users/${userID}/roles`, {
        method,
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ roleName }),
      });
      if (!res.ok) {
        const body = await res.json();
        throw new Error(body.error || `HTTP ${res.status}`);
      }
      fetchUsers();
    } catch (err) {
      alert(err instanceof Error ? err.message : "Failed to update role");
    }
  };

  if (!isAdmin) {
    return (
      <div>
        <h1 className="text-2xl font-bold mb-4">Users</h1>
        <p className="text-gray-500">
          You need platform-admin access to manage users.
        </p>
      </div>
    );
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <h1 className="text-2xl font-bold">Users</h1>
      </div>

      {loading && <p className="text-gray-500">Loading users...</p>}
      {error && <p className="text-red-600">Error: {error}</p>}

      {!loading && !error && (
        <div className="border rounded-lg overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-gray-50 border-b">
              <tr>
                <th className="text-left px-4 py-3 font-medium">User</th>
                <th className="text-left px-4 py-3 font-medium">Email</th>
                <th className="text-left px-4 py-3 font-medium">Status</th>
                <th className="text-left px-4 py-3 font-medium">Roles</th>
                <th className="text-left px-4 py-3 font-medium">Actions</th>
              </tr>
            </thead>
            <tbody className="divide-y">
              {users.map((user) => (
                <Fragment key={user.id}>
                <tr className="hover:bg-gray-50">
                    <td className="px-4 py-3">
                      <div className="font-medium">{user.username}</div>
                      <div className="text-xs text-gray-500">
                        {user.firstName} {user.lastName}
                      </div>
                    </td>
                    <td className="px-4 py-3 text-gray-500">
                      {user.email ?? "—"}
                    </td>
                    <td className="px-4 py-3">
                      <span
                        className={`inline-block px-2 py-0.5 rounded text-xs font-medium ${
                          user.enabled
                            ? "bg-green-100 text-green-800"
                            : "bg-red-100 text-red-800"
                        }`}
                      >
                        {user.enabled ? "Active" : "Disabled"}
                      </span>
                    </td>
                    <td className="px-4 py-3">
                      <div className="flex flex-wrap gap-1">
                        {(user.realmRoles ?? []).map((role) => (
                          <span
                            key={role}
                            className={`inline-block px-2 py-0.5 rounded text-xs font-medium ${roleBadgeColors[role] ?? "bg-gray-100 text-gray-700"}`}
                          >
                            {role}
                          </span>
                        ))}
                      </div>
                    </td>
                    <td className="px-4 py-3">
                      <div className="flex gap-2">
                        <button
                          onClick={() =>
                            setExpandedUser(
                              expandedUser === user.id ? null : user.id,
                            )
                          }
                          className="text-xs text-blue-600 hover:underline"
                        >
                          {expandedUser === user.id ? "Hide" : "Roles"}
                        </button>
                        <button
                          onClick={() => handleDelete(user.id, user.username)}
                          className="text-xs text-red-600 hover:underline"
                        >
                          Delete
                        </button>
                      </div>
                    </td>
                  </tr>
                  {expandedUser === user.id && (
                    <tr key={`${user.id}-roles`}>
                      <td colSpan={5} className="px-4 py-3 bg-gray-50">
                        <p className="text-xs font-medium text-gray-600 mb-2">
                          Toggle realm roles for {user.username}:
                        </p>
                        <div className="flex flex-wrap gap-2">
                          {["platform-admin", "platform-operator"].map(
                            (role) => {
                              const hasRole =
                                user.realmRoles?.includes(role) ?? false;
                              return (
                                <button
                                  key={role}
                                  onClick={() =>
                                    handleToggleRole(user.id, role, hasRole)
                                  }
                                  className={`px-3 py-1 rounded text-xs font-medium border transition-colors ${
                                    hasRole
                                      ? "bg-blue-600 text-white border-blue-600"
                                      : "bg-white text-gray-700 border-gray-300 hover:bg-gray-100"
                                  }`}
                                >
                                  {hasRole ? "\u2713 " : ""}{role}
                                </button>
                              );
                            },
                          )}
                        </div>
                      </td>
                    </tr>
                  )}
                </Fragment>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
