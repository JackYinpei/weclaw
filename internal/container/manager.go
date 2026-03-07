package container

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/qcy/weclaw/internal/config"
	"github.com/qcy/weclaw/pkg/logger"
)

// ContainerInfo holds information about a user's OpenClaw container.
type ContainerInfo struct {
	ContainerID   string
	ContainerName string
	Port          int
	GatewayToken  string
	Status        string
}

// Manager manages Docker containers for OpenClaw instances.
type Manager struct {
	cli      *client.Client
	cfg      *config.DockerConfig
	portPool *PortPool
	mu       sync.Mutex
}

// NewManager creates a new container manager.
func NewManager(cfg *config.DockerConfig) (*Manager, error) {
	cli, err := createDockerClient()
	if err != nil {
		return nil, err
	}

	logger.Info("Docker client initialized successfully")

	return &Manager{
		cli:      cli,
		cfg:      cfg,
		portPool: NewPortPool(cfg.PortRangeStart, cfg.PortRangeEnd),
	}, nil
}

// createDockerClient tries to create a Docker client, auto-detecting the socket path
// on macOS where Docker Desktop may use a non-standard location.
func createDockerClient() (*client.Client, error) {
	// If DOCKER_HOST is already set, use the default behavior
	if os.Getenv("DOCKER_HOST") != "" {
		cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		if err != nil {
			return nil, fmt.Errorf("failed to create Docker client: %w", err)
		}
		if err := pingDocker(cli); err != nil {
			return nil, err
		}
		return cli, nil
	}

	// Try default socket first, then macOS Docker Desktop locations
	homeDir, _ := os.UserHomeDir()
	socketPaths := []string{
		"/var/run/docker.sock",                                      // Linux default
		filepath.Join(homeDir, ".docker", "run", "docker.sock"),     // macOS Docker Desktop (newer)
		filepath.Join(homeDir, ".colima", "default", "docker.sock"), // Colima
		filepath.Join(homeDir, ".orbstack", "run", "docker.sock"),   // OrbStack
		"/Users/" + os.Getenv("USER") + "/.docker/run/docker.sock",  // macOS fallback
	}

	for _, sockPath := range socketPaths {
		if _, err := os.Stat(sockPath); err != nil {
			continue // Socket doesn't exist, skip
		}

		host := "unix://" + sockPath
		logger.Debug("Trying Docker socket", "path", sockPath)

		cli, err := client.NewClientWithOpts(
			client.WithHost(host),
			client.WithAPIVersionNegotiation(),
		)
		if err != nil {
			continue
		}

		if err := pingDocker(cli); err == nil {
			logger.Info("Connected to Docker daemon", "socket", sockPath)
			return cli, nil
		}
		cli.Close()
	}

	return nil, fmt.Errorf("failed to connect to Docker daemon: tried sockets %v. Is Docker running?", socketPaths)
}

// pingDocker verifies Docker daemon connectivity.
func pingDocker(cli *client.Client) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := cli.Ping(ctx)
	if err != nil {
		return fmt.Errorf("failed to ping Docker daemon: %w", err)
	}
	return nil
}

// CreateContainer creates a new OpenClaw container for a user.
func (m *Manager) CreateContainer(ctx context.Context, userOpenID string, openclawCfg *config.OpenClawConfig) (*ContainerInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Allocate a port
	port, err := m.portPool.Allocate()
	if err != nil {
		return nil, fmt.Errorf("failed to allocate port: %w", err)
	}

	idPrefix := userOpenID
	if len(idPrefix) > 12 {
		idPrefix = idPrefix[:12]
	}
	containerName := fmt.Sprintf("weclaw-openclaw-%s", idPrefix)
	gatewayToken := generateToken()

	// Parse memory limit
	var memoryBytes int64
	memStr := m.cfg.MemoryLimit
	if strings.HasSuffix(memStr, "m") || strings.HasSuffix(memStr, "M") {
		var mb int64
		fmt.Sscanf(memStr, "%d", &mb)
		memoryBytes = mb * 1024 * 1024
	} else if strings.HasSuffix(memStr, "g") || strings.HasSuffix(memStr, "G") {
		var gb int64
		fmt.Sscanf(memStr, "%d", &gb)
		memoryBytes = gb * 1024 * 1024 * 1024
	} else {
		memoryBytes = 512 * 1024 * 1024 // default 512MB
	}

	// Parse CPU limit
	var nanoCPUs int64
	var cpuFloat float64
	fmt.Sscanf(m.cfg.CPULimit, "%f", &cpuFloat)
	nanoCPUs = int64(cpuFloat * 1e9)

	// Ensure the image exists (pull if needed)
	if err := m.ensureImage(ctx); err != nil {
		m.portPool.Release(port)
		return nil, fmt.Errorf("failed to ensure image: %w", err)
	}

	// 在宿主机创建该容器专属的 .openclaw 目录并写入 openclaw.json（gateway 配置 + 模型配置）
	hostOpenClawDir, err := m.prepareOpenClawHostDir(containerName, openclawCfg)
	if err != nil {
		m.portPool.Release(port)
		return nil, fmt.Errorf("failed to prepare OpenClaw host dir: %w", err)
	}

	// Create container — Gateway serves /v1/responses on port 18789
	containerPort := nat.Port("18789/tcp")

	// Build environment variables
	// Calculate Node.js heap size: use ~75% of container memory limit
	nodeHeapMB := memoryBytes * 75 / 100 / (1024 * 1024)
	if nodeHeapMB < 256 {
		nodeHeapMB = 256
	}

	// API Key 和 Base URL 已通过 openclaw.json 的自定义 provider 注入，不需要环境变量。
	envVars := []string{
		fmt.Sprintf("OPENCLAW_GATEWAY_TOKEN=%s", gatewayToken),
		"OPENCLAW_GATEWAY_BIND=lan",
		fmt.Sprintf("NODE_OPTIONS=--max-old-space-size=%d", nodeHeapMB),
	}

	resp, err := m.cli.ContainerCreate(ctx,
		&container.Config{
			Image: m.cfg.OpenClawImage,
			Env:   envVars,
			// 仅启动 gateway，bind=lan 使宿主机可通过映射端口访问；配置已通过挂载的 config.json5 注入
			Cmd: []string{"openclaw", "gateway", "--allow-unconfigured", "--bind", "lan"},
			ExposedPorts: nat.PortSet{
				containerPort: struct{}{},
			},
			Labels: map[string]string{
				"weclaw.user":    userOpenID,
				"weclaw.managed": "true",
			},
		},
		&container.HostConfig{
			PortBindings: nat.PortMap{
				containerPort: []nat.PortBinding{
					{
						HostIP:   "127.0.0.1",
						HostPort: fmt.Sprintf("%d", port),
					},
				},
			},
			// 挂载宿主机目录到容器 ~/.openclaw，内含 config.json5（dangerouslyAllowHostHeaderOriginFallback 等）
			Binds: []string{hostOpenClawDir + ":/home/node/.openclaw"},
			Resources: container.Resources{
				Memory:   memoryBytes,
				NanoCPUs: nanoCPUs,
			},
			RestartPolicy: container.RestartPolicy{
				Name:              container.RestartPolicyOnFailure,
				MaximumRetryCount: 3,
			},
		},
		&network.NetworkingConfig{},
		nil,
		containerName,
	)
	if err != nil {
		m.portPool.Release(port)
		return nil, fmt.Errorf("failed to create container: %w", err)
	}

	// Start container
	if err := m.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		m.portPool.Release(port)
		// Try to remove the created but not started container
		_ = m.cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return nil, fmt.Errorf("failed to start container: %w", err)
	}

	logger.Info("Container created and started",
		"container_id", resp.ID[:12],
		"name", containerName,
		"port", port,
		"user", userOpenID,
	)

	return &ContainerInfo{
		ContainerID:   resp.ID,
		ContainerName: containerName,
		Port:          port,
		GatewayToken:  gatewayToken,
		Status:        "running",
	}, nil
}

// StopContainer stops a running container (sleep).
func (m *Manager) StopContainer(ctx context.Context, containerID string) error {
	timeout := 10 // seconds
	stopOptions := container.StopOptions{Timeout: &timeout}
	if err := m.cli.ContainerStop(ctx, containerID, stopOptions); err != nil {
		if client.IsErrNotFound(err) {
			logger.Warn("Container not found, treating as stopped", "container_id", containerID[:12])
			return nil
		}
		return fmt.Errorf("failed to stop container %s: %w", containerID[:12], err)
	}
	logger.Info("Container stopped", "container_id", containerID[:12])
	return nil
}

// StartContainer starts a stopped container (wake up).
func (m *Manager) StartContainer(ctx context.Context, containerID string) error {
	if err := m.cli.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		return fmt.Errorf("failed to start container %s: %w", containerID[:12], err)
	}
	logger.Info("Container started", "container_id", containerID[:12])
	return nil
}

// RemoveContainer stops and removes a container.
func (m *Manager) RemoveContainer(ctx context.Context, containerID string, port int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Stop first (ignore error if already stopped)
	timeout := 5
	stopOptions := container.StopOptions{Timeout: &timeout}
	_ = m.cli.ContainerStop(ctx, containerID, stopOptions)

	// Remove
	if err := m.cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true}); err != nil {
		return fmt.Errorf("failed to remove container %s: %w", containerID[:12], err)
	}

	// Release port
	if port > 0 {
		m.portPool.Release(port)
	}

	logger.Info("Container removed", "container_id", containerID[:12])
	return nil
}

// IsContainerRunning checks if a container is currently running.
func (m *Manager) IsContainerRunning(ctx context.Context, containerID string) (bool, error) {
	info, err := m.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return false, err
	}
	return info.State.Running, nil
}

// prepareOpenClawHostDir 在宿主机创建容器专属目录并写入 openclaw.json，返回用于 bind mount 的绝对路径。
// openclaw.json 包含 gateway LAN 绑定配置和模型 provider/model 设置。
func (m *Manager) prepareOpenClawHostDir(containerName string, openclawCfg *config.OpenClawConfig) (string, error) {
	baseDir := m.cfg.OpenClawHostDataDir
	if baseDir == "" {
		baseDir = "./data/weclaw-openclaw"
	}
	hostDir := filepath.Join(baseDir, containerName)
	if err := os.MkdirAll(hostDir, 0755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", hostDir, err)
	}

	// 构建 openclaw.json：gateway 配置 + 自定义 provider（支持 OpenAI 兼容代理的 baseUrl/apiKey）
	providerName := "weclaw-llm"
	modelSpec := fmt.Sprintf("%s/%s", providerName, openclawCfg.ModelName)

	cfgMap := map[string]any{
		"gateway": map[string]any{
			"controlUi": map[string]any{
				"dangerouslyAllowHostHeaderOriginFallback": true,
			},
		},
		"agents": map[string]any{
			"defaults": map[string]any{
				"model":  map[string]any{"primary": modelSpec},
				"models": map[string]any{modelSpec: map[string]any{}},
			},
		},
		"models": map[string]any{
			"providers": map[string]any{
				providerName: map[string]any{
					"baseUrl": openclawCfg.BaseURL + "/v1",
					"apiKey":  openclawCfg.APIKey,
					"api":     "openai-completions",
					"models": []map[string]any{
						{"id": openclawCfg.ModelName, "name": openclawCfg.ModelName},
					},
				},
			},
		},
	}

	configBytes, err := json.Marshal(cfgMap)
	if err != nil {
		return "", fmt.Errorf("marshal openclaw config: %w", err)
	}

	configPath := filepath.Join(hostDir, "openclaw.json")
	if err := os.WriteFile(configPath, append(configBytes, '\n'), 0644); err != nil {
		return "", fmt.Errorf("write %s: %w", configPath, err)
	}

	absDir, err := filepath.Abs(hostDir)
	if err != nil {
		return "", fmt.Errorf("abs path %s: %w", hostDir, err)
	}
	logger.Debug("Prepared OpenClaw host dir", "path", absDir, "model", modelSpec)
	return absDir, nil
}

// ensureImage makes sure the OpenClaw image is available locally.
func (m *Manager) ensureImage(ctx context.Context) error {
	_, _, err := m.cli.ImageInspectWithRaw(ctx, m.cfg.OpenClawImage)
	if err == nil {
		return nil // Image already exists
	}

	logger.Info("Pulling OpenClaw image", "image", m.cfg.OpenClawImage)
	reader, err := m.cli.ImagePull(ctx, m.cfg.OpenClawImage, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("failed to pull image %s: %w", m.cfg.OpenClawImage, err)
	}
	defer reader.Close()

	// Read the output to completion
	_, _ = io.Copy(io.Discard, reader)

	logger.Info("Image pulled successfully", "image", m.cfg.OpenClawImage)
	return nil
}

// Close cleans up the Docker client.
func (m *Manager) Close() error {
	return m.cli.Close()
}

// generateToken generates a random token for the OpenClaw gateway.
func generateToken() string {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	if err != nil {
		// Fallback to timestamp-based token
		return fmt.Sprintf("weclaw-gw-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("weclaw-gw-%x", b)
}
