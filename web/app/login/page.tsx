"use client";

import { signIn } from "next-auth/react";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";

function LoginForm() {
  const router = useRouter();
  const searchParams = useSearchParams();

  // Only ever redirect back to an internal path after auth. The raw query
  // value is attacker-controlled: `/login?callbackUrl=https://evil.com` would
  // otherwise send a successfully-authenticated browser off-host. A leading
  // "/" that isn't "//" also blocks protocol-relative URLs.
  const rawCallback = searchParams.get("callbackUrl");
  const internalPath =
    rawCallback && rawCallback.startsWith("/") && !rawCallback.startsWith("//")
      ? rawCallback
      : undefined;
  const callbackUrl = internalPath ?? "/dashboard";

  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);

  async function handleSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError(null);
    setPending(true);

    try {
      const res = await signIn("credentials", {
        username,
        password,
        redirect: false,
        redirectTo: callbackUrl,
      });

      if (!res?.error && (res?.ok || res?.url)) {
        router.push(callbackUrl);
        router.refresh();
        return;
      }

      setError("Invalid username or password");
      setPassword("");
    } catch {
      setError("Invalid username or password");
      setPassword("");
    } finally {
      setPending(false);
    }
  }

  return (
    <div className="flex flex-1 items-center justify-center px-4 py-12">
      <main className="flex w-full max-w-sm flex-col items-center gap-6">
        <div className="flex flex-col items-center gap-3">
          <div className="flex h-14 w-14 items-center justify-center rounded-2xl bg-primary text-2xl font-bold text-primary-foreground">
            T
          </div>
          <h1 className="text-2xl font-bold tracking-tight text-foreground">
            TenantFlow
          </h1>
          <p className="text-muted-foreground">
            Multi-tenant SaaS control plane
          </p>
        </div>

        <Card className="w-full">
          <CardHeader>
            <CardTitle>Sign in</CardTitle>
            <CardDescription>
              Enter your account credentials to continue.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <form onSubmit={handleSubmit} className="flex flex-col gap-4">
              <div className="flex flex-col gap-1.5">
                <label
                  htmlFor="username"
                  className="text-xs font-medium text-foreground"
                >
                  Username or email
                </label>
                <Input
                  id="username"
                  name="username"
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  autoComplete="username"
                  autoFocus
                  required
                  disabled={pending}
                  placeholder="tenantflow-admin"
                />
              </div>

              <div className="flex flex-col gap-1.5">
                <label
                  htmlFor="password"
                  className="text-xs font-medium text-foreground"
                >
                  Password
                </label>
                <Input
                  id="password"
                  name="password"
                  type="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  autoComplete="current-password"
                  required
                  disabled={pending}
                />
              </div>

              {error && (
                <p
                  role="alert"
                  className="rounded-md bg-destructive/10 px-3 py-2 text-sm text-destructive"
                >
                  {error}
                </p>
              )}

              <Button type="submit" size="lg" disabled={pending}>
                {pending ? (
                  <>
                    <Loader2 className="animate-spin" />
                    Signing in…
                  </>
                ) : (
                  "Sign in"
                )}
              </Button>
            </form>
          </CardContent>
        </Card>
      </main>
    </div>
  );
}

// `useSearchParams` opts the tree out of prerendering, so it needs a boundary
// of its own to keep the rest of the page static.
export default function LoginPage() {
  return (
    <Suspense
      fallback={
        <div className="flex flex-1 items-center justify-center">
          <p className="text-sm text-muted-foreground">Loading…</p>
        </div>
      }
    >
      <LoginForm />
    </Suspense>
  );
}