"use client";

import { useSession, signOut } from "next-auth/react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { useTheme } from "next-themes";
import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import {
  LayoutDashboard,
  Users,
  Server,
  LogOut,
  Sun,
  Moon,
  PanelLeftClose,
  PanelLeftOpen,
} from "lucide-react";

const navItems = [
  { href: "/dashboard", label: "Overview", icon: LayoutDashboard },
  { href: "/dashboard/tenants", label: "Tenants", icon: Server },
];

function ThemeToggle({ collapsed }: { collapsed: boolean }) {
  const { theme, setTheme } = useTheme();
  const [mounted, setMounted] = useState(false);

  useEffect(() => setMounted(true), []);

  if (!mounted) return null;

  const toggle = () => setTheme(theme === "dark" ? "light" : "dark");

  if (collapsed) {
    return (
      <div className="flex justify-center">
        <Tooltip>
          <TooltipTrigger
            render={<Button variant="ghost" size="icon" className="h-8 w-8" onClick={toggle} />}
          >
            {theme === "dark" ? (
              <Sun className="h-4 w-4" />
            ) : (
              <Moon className="h-4 w-4" />
            )}
          </TooltipTrigger>
          <TooltipContent side="right">Toggle theme</TooltipContent>
        </Tooltip>
      </div>
    );
  }

  return (
    <Button
      variant="ghost"
      size="sm"
      className="w-full justify-start gap-2"
      onClick={toggle}
    >
      {theme === "dark" ? (
        <Sun className="h-4 w-4" />
      ) : (
        <Moon className="h-4 w-4" />
      )}
      <span>Theme</span>
    </Button>
  );
}

export default function DashboardLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  const { data: session } = useSession();
  const pathname = usePathname();
  const [collapsed, setCollapsed] = useState(false);

  const user = session?.user;
  const isAdmin = user?.realmRoles?.includes("platform-admin") ?? false;

  return (
    <TooltipProvider delay={0}>
      <div className="flex h-screen overflow-hidden bg-background">
        {/* Sidebar */}
        <aside
          className={`flex flex-col border-r border-border bg-sidebar transition-all duration-200 ${
            collapsed ? "w-16" : "w-60"
          }`}
        >
          {/* Logo + collapse toggle */}
          <div className="flex h-14 items-center justify-between border-b border-border px-3">
            {!collapsed && (
              <Link
                href="/dashboard"
                className="text-lg font-bold text-sidebar-primary"
              >
                TenantFlow
              </Link>
            )}
            <Button
              variant="ghost"
              size="icon"
              className="h-8 w-8 shrink-0"
              onClick={() => setCollapsed(!collapsed)}
            >
              {collapsed ? (
                <PanelLeftOpen className="h-4 w-4" />
              ) : (
                <PanelLeftClose className="h-4 w-4" />
              )}
            </Button>
          </div>

          {/* Navigation */}
          <nav className="flex-1 space-y-1 px-2 py-3">
            {navItems.map((item) => {
              const isActive =
                item.href === "/dashboard"
                  ? pathname === "/dashboard"
                  : pathname.startsWith(item.href);

              if (collapsed) {
                return (
                  <Tooltip key={item.href}>
                    <TooltipTrigger
                      render={
                        <Link
                          href={item.href}
                          className={`flex items-center justify-center rounded-md px-2.5 py-2 text-sm font-medium transition-colors ${
                            isActive
                              ? "bg-sidebar-primary text-sidebar-primary-foreground"
                              : "text-sidebar-foreground hover:bg-sidebar-accent hover:text-sidebar-accent-foreground"
                          }`}
                        />
                      }
                    >
                      <item.icon className="h-4 w-4" />
                    </TooltipTrigger>
                    <TooltipContent side="right">{item.label}</TooltipContent>
                  </Tooltip>
                );
              }

              return (
                <Link
                  key={item.href}
                  href={item.href}
                  className={`flex items-center gap-2 rounded-md px-2.5 py-2 text-sm font-medium transition-colors ${
                    isActive
                      ? "bg-sidebar-primary text-sidebar-primary-foreground"
                      : "text-sidebar-foreground hover:bg-sidebar-accent hover:text-sidebar-accent-foreground"
                  }`}
                >
                  <item.icon className="h-4 w-4 shrink-0" />
                  <span>{item.label}</span>
                </Link>
              );
            })}

            {/* Admin section */}
            {isAdmin && (
              <>
                <Separator className="my-2" />
                {!collapsed && (
                  <p className="px-2.5 py-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
                    Admin
                  </p>
                )}
                {(() => {
                  const isActive = pathname.startsWith("/dashboard/users");

                  if (collapsed) {
                    return (
                      <Tooltip>
                        <TooltipTrigger
                          render={
                            <Link
                              href="/dashboard/users"
                              className={`flex items-center justify-center rounded-md px-2.5 py-2 text-sm font-medium transition-colors ${
                                isActive
                                  ? "bg-sidebar-primary text-sidebar-primary-foreground"
                                  : "text-sidebar-foreground hover:bg-sidebar-accent hover:text-sidebar-accent-foreground"
                              }`}
                            />
                          }
                        >
                          <Users className="h-4 w-4" />
                        </TooltipTrigger>
                        <TooltipContent side="right">Users</TooltipContent>
                      </Tooltip>
                    );
                  }

                  return (
                    <Link
                      href="/dashboard/users"
                      className={`flex items-center gap-2 rounded-md px-2.5 py-2 text-sm font-medium transition-colors ${
                        isActive
                          ? "bg-sidebar-primary text-sidebar-primary-foreground"
                          : "text-sidebar-foreground hover:bg-sidebar-accent hover:text-sidebar-accent-foreground"
                      }`}
                    >
                      <Users className="h-4 w-4 shrink-0" />
                      <span>Users</span>
                    </Link>
                  );
                })()}
              </>
            )}
          </nav>

          {/* Bottom section: theme toggle + user + sign out */}
          <div className="border-t border-border p-2">
            <ThemeToggle collapsed={collapsed} />
            <Separator className="my-2" />

            {/* User info */}
            {!collapsed ? (
              <div className="flex items-center gap-2 rounded-md px-2.5 py-2">
                <div className="flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-primary text-xs font-bold text-primary-foreground">
                  {user?.name?.[0] ?? user?.email?.[0] ?? "?"}
                </div>
                <div className="flex-1 min-w-0">
                  <p className="truncate text-xs font-medium text-sidebar-foreground">
                    {user?.name ?? "Unknown"}
                  </p>
                  <p className="truncate text-[10px] text-muted-foreground">
                    {user?.email ?? ""}
                  </p>
                </div>
              </div>
            ) : (
              <Tooltip>
                <TooltipTrigger
                  render={
                    <div className="flex justify-center py-1" />
                  }
                >
                  <div className="flex h-7 w-7 items-center justify-center rounded-full bg-primary text-xs font-bold text-primary-foreground">
                    {user?.name?.[0] ?? user?.email?.[0] ?? "?"}
                  </div>
                </TooltipTrigger>
                <TooltipContent side="right">
                  {user?.name ?? "Unknown"}
                </TooltipContent>
              </Tooltip>
            )}

            {/* Sign out */}
            {collapsed ? (
              <div className="flex justify-center">
                <Tooltip>
                  <TooltipTrigger
                    render={
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-8 w-8 text-muted-foreground hover:text-destructive"
                        onClick={() => signOut({ callbackUrl: "/" })}
                      />
                    }
                  >
                    <LogOut className="h-4 w-4" />
                  </TooltipTrigger>
                  <TooltipContent side="right">Sign out</TooltipContent>
                </Tooltip>
              </div>
            ) : (
              <Button
                variant="ghost"
                size="sm"
                className="w-full justify-start gap-2 text-muted-foreground hover:text-destructive"
                onClick={() => signOut({ callbackUrl: "/" })}
              >
                <LogOut className="h-4 w-4 shrink-0" />
                <span>Sign out</span>
              </Button>
            )}
          </div>
        </aside>

        {/* Main content */}
        <main className="flex min-h-0 flex-1 flex-col p-6 lg:p-8">{children}</main>
      </div>
    </TooltipProvider>
  );
}
