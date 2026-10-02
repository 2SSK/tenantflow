import NextAuth from "next-auth";
import Credentials from "next-auth/providers/credentials";

/** Claims read out of a Keycloak access token. */
type AccessTokenIdentity = {
  sub?: string;
  name?: string;
  preferredUsername?: string;
  email?: string;
  realmRoles: string[];
};

/** Decode a Keycloak access token payload. Trusted (server-fetched), so no signature check. */
function readAccessToken(accessToken: string): AccessTokenIdentity {
  const payload = accessToken.split(".")[1];
  if (!payload) return { realmRoles: [] };

  try {
    const claims = JSON.parse(
      Buffer.from(payload, "base64url").toString(),
    ) as {
      sub?: string;
      name?: string;
      preferred_username?: string;
      email?: string;
      realm_access?: { roles?: unknown };
    };

    const roles = claims.realm_access?.roles;
    return {
      sub: claims.sub,
      name: claims.name,
      preferredUsername: claims.preferred_username,
      email: claims.email,
      realmRoles: Array.isArray(roles)
        ? roles.filter((role): role is string => typeof role === "string")
        : [],
    };
  } catch {
    // Failed to decode — continue without profile data.
    return { realmRoles: [] };
  }
}

export const { handlers, auth, signIn, signOut } = NextAuth({
  providers: [
    Credentials({
      credentials: {
        username: { label: "Username or email", type: "text" },
        password: { label: "Password", type: "password" },
      },

      // Keycloak password grant; the token endpoint resolves username or email.
      async authorize(credentials) {
        const username =
          typeof credentials.username === "string"
            ? credentials.username.trim()
            : "";
        const password =
          typeof credentials.password === "string" ? credentials.password : "";

        if (!username || !password) return null;

        const issuer = process.env.AUTH_KEYCLOAK_ISSUER;
        if (!issuer) {
          throw new Error("AUTH_KEYCLOAK_ISSUER is not configured");
        }

        const response = await fetch(
          `${issuer}/protocol/openid-connect/token`,
          {
            method: "POST",
            headers: {
              "Content-Type": "application/x-www-form-urlencoded",
            },
            body: new URLSearchParams({
              grant_type: "password",
              client_id: process.env.AUTH_KEYCLOAK_ID ?? "",
              client_secret: process.env.AUTH_KEYCLOAK_SECRET ?? "",
              username,
              password,
            }),
          },
        );

        // Any rejection is reported as a generic failure — never leak which part was wrong.
        if (!response.ok) return null;

        const { access_token } = (await response.json()) as {
          access_token?: string;
        };
        if (!access_token) return null;

        const identity = readAccessToken(access_token);

        return {
          id: identity.sub ?? username,
          name: identity.name ?? identity.preferredUsername ?? username,
          email: identity.email ?? "",
          accessToken: access_token,
          realmRoles: identity.realmRoles,
        };
      },
    }),
  ],

  session: {
    strategy: "jwt",
  },

  callbacks: {
    // Credentials sign-ins surface the authorize() user here, on first sign-in only.
    async jwt({ token, user }) {
      if (user) {
        return {
          ...token,
          accessToken: user.accessToken ?? "",
          userId: user.id ?? "",
          email: user.email ?? "",
          name: user.name ?? "",
          realmRoles: user.realmRoles ?? [],
        };
      }
      return token;
    },

    async session({ session, token }) {
      return {
        ...session,
        user: {
          ...session.user,
          id: token.userId as string,
          accessToken: token.accessToken as string,
          realmRoles: token.realmRoles as string[],
        },
      };
    },
  },

  pages: {
    signIn: "/login",
  },
});

declare module "next-auth" {
  interface User {
    accessToken?: string;
    realmRoles?: string[];
  }

  interface Session {
    user: {
      id: string;
      accessToken: string;
      realmRoles: string[];
      name?: string | null;
      email?: string | null;
      image?: string | null;
    };
  }
}

declare module "@auth/core/jwt" {
  interface JWT {
    accessToken?: string;
    userId?: string;
    email?: string;
    name?: string;
    realmRoles?: string[];
  }
}