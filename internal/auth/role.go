package auth

import (
	"encoding/json"
	"net/http"
)

func RequireRole(requiredRole string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		roles := GetUserRoles(r.Context())

		for _, role := range roles {
			if role == requiredRole {
				next.ServeHTTP(w, r)
				return
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		resp := map[string]string{"error": "forbidden: requires role " + requiredRole}
		json.NewEncoder(w).Encode(resp)
	})
}
