package auth

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type Provider struct {
	verifier *oidc.IDTokenVerifier
	config   oauth2.Config
	log      *slog.Logger

	ready bool
	mu    sync.RWMutex
}

func NewProvider(ctx context.Context, keycloakURL, realm, clientID, clientSecret, redirectURL string, log *slog.Logger) (*Provider, error) {
	issuerURL := fmt.Sprintf("%s/realms/%s", keycloakURL, realm)

	oidcProvider, err := oidc.NewProvider(ctx, issuerURL)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery %s: %w", issuerURL, err)
	}

	verifier := oidcProvider.Verifier(&oidc.Config{
		ClientID:          clientID,
		SkipClientIDCheck: true,
	})

	config := oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     oidcProvider.Endpoint(),
		RedirectURL:  redirectURL,
		Scopes:       []string{"openid", "profile", "email"},
	}

	log.Info("oidc provider initialized", "issuer", issuerURL, "clientID", clientID)

	return &Provider{
		verifier: verifier,
		config:   config,
		log:      log,
		ready:    true,
	}, nil
}

type Claims struct {
	Sub   string `json:"sub"`
	Iss   string `json:"iss"`
	Exp   int64  `json:"exp"`
	Iat   int64  `json:"iat"`
	Nonce string `json:"nonce"`

	PreferredUsername string `json:"preferred_username"`
	Email             string `json:"email"`
	Name              string `json:"name"`
	RealmAccess       struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
}

func (p *Provider) VerifyToken(ctx context.Context, token string) (*Claims, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if !p.ready {
		return nil, fmt.Errorf("oidc provider not initialized")
	}

	idToken, err := p.verifier.Verify(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("verify token: %w", err)
	}

	var claims Claims
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("parse claims: %w", err)
	}

	return &claims, nil
}

func ExtractToken(r *http.Request) (string, error) {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return "", fmt.Errorf("missing authorization header")
	}

	parts := strings.SplitN(auth, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", fmt.Errorf("invalid authorization header format")
	}

	token := strings.TrimSpace(parts[1])
	if token == "" {
		return "", fmt.Errorf("empty bearer token")
	}

	return token, nil
}

func (p *Provider) ExchangeCode(ctx context.Context, code string) (*oauth2.Token, error) {
	return p.config.Exchange(ctx, code)
}

func (p *Provider) RefreshToken(ctx context.Context, refreshToken string) (*oauth2.Token, error) {
	token := &oauth2.Token{RefreshToken: refreshToken}
	tokenSource := p.config.TokenSource(ctx, token)
	return tokenSource.Token()
}

type TokenInfo struct {
	Subject   string
	Username  string
	Roles     []string
	ExpiresAt time.Time
	IssuedAt  time.Time
}
