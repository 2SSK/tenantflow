import { NextResponse } from "next/server";
import type { NextRequest } from "next/server";
import { auth } from "@/lib/auth";

const protectedRoutes = ["/dashboard"];

export async function proxy(request: NextRequest) {
  const { pathname } = request.nextUrl;

  const isProtected = protectedRoutes.some((route) =>
    pathname.startsWith(route),
  );

  if (!isProtected) {
    return NextResponse.next();
  }

  const session = await auth();

  if (!session) {
    const signInUrl = new URL("/login", request.url);
    signInUrl.searchParams.set("callbackUrl", pathname);
    return NextResponse.redirect(signInUrl);
  }

  return NextResponse.next();
}

export const config = {
  matcher: ["/((?!api|_next/static|_next/image|favicon.ico|.*\\.svg$).*)"],
};