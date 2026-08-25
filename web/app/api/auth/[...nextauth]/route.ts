/**
 * Auth.js API Route Handler.
 *
 * This catch-all route handles all Auth.js endpoints:
 *   - /api/auth/signin - Redirects to Keycloak login
 *   - /api/auth/signout - Clears session and redirects to Keycloak logout
 *   - /api/auth/callback/keycloak - Handles the OAuth callback from Keycloak
 *   - /api/auth/session - Returns the current session
 *   - /api/auth/csrf - Returns CSRF token
 *
 * The `[...nextauth]` catch-all pattern means this single route handles
 * all requests that start with `/api/auth/`.
 */
import { handlers } from "@/lib/auth";

// Export the GET and POST handlers from Auth.js
export const { GET, POST } = handlers;
