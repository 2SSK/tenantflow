import NextAuth from "next-auth";
import Keycloak from "next-auth/providers/keycloak";

export const { handlers, auth, signIn, signOut } = NextAuth({
  providers: [
    Keycloak({
      clientId: process.env.AUTH_KEYCLOAK_ID!,
      clientSecret: process.env.AUTH_KEYCLOAK_SECRET!,
      issuer: process.env.AUTH_KEYCLOAK_ISSUER,
    }),
  ],

  session: {
    strategy: "jwt",
  },

  callbacks: {
    async jwt({ token, account, profile }) {
      if (account && profile) {
        // Decode access token to get Keycloak realm_access roles
        // (realm_access is NOT in the ID token / profile — only in the access token)
        let realmRoles: string[] = [];
        if (account.access_token) {
          try {
            const payload = JSON.parse(
              Buffer.from(
                account.access_token.split(".")[1],
                "base64url",
              ).toString(),
            );
            realmRoles = payload.realm_access?.roles ?? [];
          } catch {
            // Failed to decode — continue without roles
          }
        }

        return {
          ...token,
          accessToken: account.access_token ?? "",
          userId: profile.sub ?? "",
          email: profile.email ?? "",
          name: profile.name ?? "",
          realmRoles,
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
    // signIn: "/auth/signin",
    // signOut: "/auth/signout",
  },
});

declare module "next-auth" {
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
