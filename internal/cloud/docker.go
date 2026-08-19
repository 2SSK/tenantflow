package cloud

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

type DockerProvider struct {
	client *client.Client
	log    *slog.Logger
}

func NewDockerProvider(log *slog.Logger) (*DockerProvider, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}

	return &DockerProvider{client: cli, log: log}, nil
}

func (d *DockerProvider) CreateDatabase(ctx context.Context, tenantID string) error {
	dbName := "tenant_" + tenantID
	d.log.Info("creating tenant database", "database", dbName)

	return d.execPostgres(ctx, fmt.Sprintf("CREATE DATABASE %s", dbName))
}

func (d *DockerProvider) DropDatabase(ctx context.Context, tenantID string) error {
	dbName := "tenant_" + tenantID
	d.log.Info("dropping tenant database", "database", dbName)

	d.execPostgres(ctx, fmt.Sprintf("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s'", dbName))

	return d.execPostgres(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", dbName))
}

func (d *DockerProvider) execPostgres(ctx context.Context, sql string) error {
	containers, err := d.client.ContainerList(ctx, container.ListOptions{
		All: false,
	})
	if err != nil {
		return fmt.Errorf("list containers: %w", err)
	}

	var postgresID string
	for _, c := range containers {
		for _, name := range c.Names {
			if strings.Contains(name, "postgres") {
				postgresID = c.ID
				break
			}
		}
		if postgresID != "" {
			break
		}
	}
	if postgresID == "" {
		return fmt.Errorf("no running postgres container found")
	}

	execResp, err := d.client.ContainerExecCreate(ctx, postgresID, container.ExecOptions{
		Cmd:          []string{"psql", "-U", "temporal", "-c", sql},
		AttachStdout: true,
		AttachStderr: true,
	})

	if err != nil {
		return fmt.Errorf("exec create: %w", err)
	}

	if err := d.client.ContainerExecStart(ctx, execResp.ID, container.ExecStartOptions{}); err != nil {
		return fmt.Errorf("exec start: %w", err)
	}

	inspect, err := d.client.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return fmt.Errorf("exec inspect: %w", err)
	}

	if inspect.ExitCode != 0 {
		return fmt.Errorf("psql exited with code %d running: %s", inspect.ExitCode, sql)
	}

	return nil
}
