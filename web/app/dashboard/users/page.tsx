"use client";

import { useSession } from "next-auth/react";
import { Fragment, useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Card, CardContent } from "@/components/ui/card";
import { type KcUser } from "@/lib/types";
import { Loader2, ChevronDown, ChevronRight, Trash2 } from "lucide-react";

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
      <div className="space-y-4">
        <h1 className="text-2xl font-bold">Users</h1>
        <p className="text-sm text-muted-foreground">
          You need platform-admin access to manage users.
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold">Users</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Manage Keycloak users and their realm roles.
        </p>
      </div>

      {loading && (
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" />
          Loading users...
        </div>
      )}
      {error && <p className="text-sm text-destructive">{error}</p>}

      {!loading && !error && (
        <Card>
          <CardContent className="p-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>User</TableHead>
                  <TableHead>Email</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Roles</TableHead>
                  <TableHead className="w-[100px]">Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {users.map((user) => (
                  <Fragment key={user.id}>
                    <TableRow>
                      <TableCell>
                        <div className="flex items-center gap-2">
                          <Button
                            variant="ghost"
                            size="icon"
                            className="h-5 w-5"
                            onClick={() =>
                              setExpandedUser(
                                expandedUser === user.id ? null : user.id,
                              )
                            }
                          >
                            {expandedUser === user.id ? (
                              <ChevronDown className="h-3 w-3" />
                            ) : (
                              <ChevronRight className="h-3 w-3" />
                            )}
                          </Button>
                          <div>
                            <div className="font-medium">{user.username}</div>
                            <div className="text-xs text-muted-foreground">
                              {user.firstName} {user.lastName}
                            </div>
                          </div>
                        </div>
                      </TableCell>
                      <TableCell className="text-muted-foreground">
                        {user.email ?? "\u2014"}
                      </TableCell>
                      <TableCell>
                        <Badge
                          variant="outline"
                          className={
                            user.enabled
                              ? "bg-emerald-500/10 text-emerald-500 border-emerald-500/20"
                              : "bg-destructive/10 text-destructive border-destructive/20"
                          }
                        >
                          {user.enabled ? "Active" : "Disabled"}
                        </Badge>
                      </TableCell>
                      <TableCell>
                        <div className="flex flex-wrap gap-1">
                          {(user.realmRoles ?? []).map((role) => (
                            <Badge key={role} variant="secondary">
                              {role}
                            </Badge>
                          ))}
                        </div>
                      </TableCell>
                      <TableCell>
                        <Button
                          variant="ghost"
                          size="icon"
                          className="h-8 w-8 text-muted-foreground hover:text-destructive"
                          onClick={() => handleDelete(user.id, user.username)}
                        >
                          <Trash2 className="h-4 w-4" />
                        </Button>
                      </TableCell>
                    </TableRow>

                    {/* Expanded role toggle row */}
                    {expandedUser === user.id && (
                      <TableRow>
                        <TableCell colSpan={5} className="bg-muted/50">
                          <p className="text-xs font-medium text-muted-foreground mb-2">
                            Toggle realm roles for {user.username}:
                          </p>
                          <div className="flex flex-wrap gap-2">
                            {["platform-admin", "platform-operator"].map(
                              (role) => {
                                const hasRole =
                                  user.realmRoles?.includes(role) ?? false;
                                return (
                                  <Button
                                    key={role}
                                    variant={hasRole ? "default" : "outline"}
                                    size="sm"
                                    className="h-7 text-xs"
                                    onClick={() =>
                                      handleToggleRole(
                                        user.id,
                                        role,
                                        hasRole,
                                      )
                                    }
                                  >
                                    {hasRole ? "\u2713 " : ""}
                                    {role}
                                  </Button>
                                );
                              },
                            )}
                          </div>
                        </TableCell>
                      </TableRow>
                    )}
                  </Fragment>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      )}
    </div>
  );
}
