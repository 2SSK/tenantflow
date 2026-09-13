package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

type KeycloakProvider struct {
	serverURL  string
	realm      string
	adminUser  string
	adminPass  string
	clientID   string
	httpClient *http.Client

	adminToken     string
	tokenExpiresAt time.Time
	mu             sync.Mutex
}

func NewKeycloakProvider(serverURL, realm, adminUser, adminPass string) *KeycloakProvider {
	return &KeycloakProvider{
		serverURL:  serverURL,
		realm:      realm,
		adminUser:  adminUser,
		adminPass:  adminPass,
		clientID:   "admin-cli",
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

func (k *KeycloakProvider) getAdminToken(ctx context.Context) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	if k.adminToken != "" && time.Now().Add(30*time.Second).Before(k.tokenExpiresAt) {
		return k.adminToken, nil
	}

	data := url.Values{}
	data.Set("grant_type", "password")
	data.Set("client_id", k.clientID)
	data.Set("username", k.adminUser)
	data.Set("password", k.adminPass)

	req, err := http.NewRequestWithContext(ctx, "POST", k.serverURL+"/realms/master/protocol/openid-connect/token",
		bytes.NewBufferString(data.Encode()))
	if err != nil {
		return "", fmt.Errorf("create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := k.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("keycloak token request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("keycloak token %d:%s", resp.StatusCode, body)
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}

	k.adminToken = tokenResp.AccessToken
	k.tokenExpiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)

	return k.adminToken, nil
}

func (k *KeycloakProvider) invalidateToken() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.adminToken = ""
	k.tokenExpiresAt = time.Time{}
}

func (k *KeycloakProvider) doAdminRequest(ctx context.Context, method, path string, body interface{}) (*http.Response, error) {
	token, err := k.getAdminToken(ctx)
	if err != nil {
		return nil, err
	}

	resp, err := k.doAdminRequestWithToken(ctx, token, method, path, body)
	if err != nil {
		return resp, err
	}

	// Keycloak rejects an access token when the SSO session it was issued for
	// idle-times out (master-realm session idle timeout is shorter than the
	// token lifespan, so a cached token can 401 long before its JWT exp).
	// The token may also be stale after a clock skew or a session revocation.
	// Invalidate the cache and retry exactly once with a fresh token — a new
	// password grant creates a new session, so this is self-healing. Exactly
	// one retry: a genuine auth problem (e.g. the admin lost their role)
	// surfaces the 401 to the caller, which is the current behavior anyway.
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		k.invalidateToken()
		token, err = k.getAdminToken(ctx)
		if err != nil {
			return nil, err
		}
		return k.doAdminRequestWithToken(ctx, token, method, path, body)
	}

	return resp, nil
}

func (k *KeycloakProvider) doAdminRequestWithToken(ctx context.Context, token, method, path string, body interface{}) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		jsonBytes, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewBuffer(jsonBytes)
	}

	req, err := http.NewRequestWithContext(ctx, method, k.serverURL+"/admin/realms/"+k.realm+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create admin request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return k.httpClient.Do(req)
}

// GetUserByUsername resolves a user's ID from their exact username. Keycloak
// enforces unique usernames, so at most one match can exist. This is the
// "check" half of the get-or-create pattern: provisioning first probes here,
// and only POSTs /users when the user is truly absent — a retried activity
// then converges instead of dying with HTTP 409 on a duplicate create.
func (k *KeycloakProvider) GetUserByUsername(ctx context.Context, username string) (string, bool, error) {
	resp, err := k.doAdminRequest(ctx, "GET",
		"/users?username="+url.QueryEscape(username)+"&exact=true", nil)
	if err != nil {
		return "", false, fmt.Errorf("get user by username: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", false, fmt.Errorf("get user by username %d:%s", resp.StatusCode, body)
	}

	var users []struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		return "", false, fmt.Errorf("decode users: %w", err)
	}
	if len(users) == 0 {
		return "", false, nil
	}
	return users[0].ID, true, nil
}

func (k *KeycloakProvider) CreateUser(ctx context.Context, username, email, password, firstName, lastName string) (string, error) {
	payload := map[string]interface{}{
		"username":      username,
		"email":         email,
		"firstName":     firstName,
		"lastName":      lastName,
		"enabled":       true,
		"emailVerified": true,
	}

	resp, err := k.doAdminRequest(ctx, "POST", "/users", payload)
	if err != nil {
		return "", fmt.Errorf("create user: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("create user %d:%s", resp.StatusCode, body)
	}

	location := resp.Header.Get("Location")
	if location == "" {
		return "", fmt.Errorf("create user: no location header")
	}

	userID := location[len(location)-36:]

	pwdBody := map[string]interface{}{
		"type":      "password",
		"value":     password,
		"temporary": false,
	}
	pwdResp, err := k.doAdminRequest(ctx, "PUT", "/users/"+userID+"/reset-password", pwdBody)
	if err != nil {
		return "", fmt.Errorf("set password: %w", err)
	}
	defer pwdResp.Body.Close()

	if pwdResp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(pwdResp.Body)
		return "", fmt.Errorf("set password %d:%s", pwdResp.StatusCode, body)
	}

	return userID, nil
}

func (k *KeycloakProvider) DeleteUser(ctx context.Context, userID string) error {
	resp, err := k.doAdminRequest(ctx, "DELETE", "/users/"+userID, nil)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete user %d:%s", resp.StatusCode, body)
	}

	return nil
}

func (k *KeycloakProvider) AssignRole(ctx context.Context, userID, roleName string) error {
	roleResp, err := k.doAdminRequest(ctx, "GET", "/roles/"+roleName, nil)
	if err != nil {
		return fmt.Errorf("get role: %w", err)
	}
	defer roleResp.Body.Close()

	if roleResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(roleResp.Body)
		return fmt.Errorf("get role %d:%s", roleResp.StatusCode, body)
	}

	var role struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(roleResp.Body).Decode(&role); err != nil {
		return fmt.Errorf("decode role: %w", err)
	}

	assignment := []map[string]interface{}{
		{"id": role.ID, "name": role.Name},
	}
	assignResp, err := k.doAdminRequest(ctx, "POST", "/users/"+userID+"/role-mappings/realm", assignment)
	if err != nil {
		return fmt.Errorf("assign role: %w", err)
	}
	defer assignResp.Body.Close()

	if assignResp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(assignResp.Body)
		return fmt.Errorf("assign role %d:%s", assignResp.StatusCode, body)
	}

	return nil
}
