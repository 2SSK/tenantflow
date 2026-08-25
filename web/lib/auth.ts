import NextAuth from "next-auth";
import Keycloak from "next-auth/providers/keycloak";

export const { handlers, auth, signIn, signOut } = NextAuth({
  debug: true,

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
        const kcProfile = profile as Record<string, unknown>;
        const realmAccess = kcProfile.realm_access as
          { roles?: string[] } | undefined;

        return {
          ...token,
          accessToken: account.access_token ?? "",
          userId: profile.sub ?? "",
          email: profile.email ?? "",
          name: profile.name ?? "",
          realmRoles: realmAccess?.roles ?? [],
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
