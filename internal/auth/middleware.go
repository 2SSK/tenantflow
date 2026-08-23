package auth

import (
	"context"
	"net/http"
)

type contextKey string

const (
	UserIDKey   contextKey = "user_id"
	UserRolesKey contextKey = "user_roles"
)

func RequireAuth(provider *Provider, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, err := ExtractToken(r)
		if err != nil {
			writeUnauthorized(w, "missing or invalid token")
			return
		}

		claims, err := provider.VerifyToken(r.Context(), token)
		if err != nil {
			provider.log.Error("token verification failed", "error", err)
			writeUnauthorized(w, "invalid token")
			return
		}

		ctx := context.WithValue(r.Context(), UserIDKey, claims.Sub)

		ctx = context.WithValue(ctx, UserRolesKey, claims.RealmAccess.Roles)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func GetUserID(ctx context.Context) string {
	if v, ok := ctx.Value(UserIDKey).(string); ok {
		return v
	}
	return ""
}

func GetUserRoles(ctx context.Context) []string {
	if v, ok := ctx.Value(UserRolesKey).([]string); ok {
		return v
	}
	return nil
}

func writeUnauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	w.Write([]byte(`{"error": "` + msg + `"}`))
}
