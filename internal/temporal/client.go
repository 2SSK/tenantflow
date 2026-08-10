package temporal

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/2SSK/tenantflow/internal/config"
	"go.temporal.io/sdk/client"
	sdklog "go.temporal.io/sdk/log"
)

type Client struct {
	client.Client
}

func New(ctx context.Context, cfg config.Config, log *slog.Logger) (*Client, error) {
	c, err := client.Dial(client.Options{
		HostPort:  cfg.TemporalAddress,
		Namespace: cfg.TemporalNamespace,
		Logger:    sdklog.NewStructuredLogger(log),
	})
	if err != nil {
		return nil, fmt.Errorf("dial temaporal at %s: %w", cfg.TemporalAddress, err)
	}

	hctx, cance := context.WithTimeout(ctx, 5*time.Second)
	defer cance()

	if _, err := c.CheckHealth(hctx, nil); err != nil {
		c.Close()
		return nil, fmt.Errorf("temporal health check: %w", err)
	}

	return &Client{Client: c}, nil
}
