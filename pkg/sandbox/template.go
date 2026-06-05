package sandbox

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/fanlv/quartet/pkg/logger"
	"gopkg.in/yaml.v3"
)

// sandboxServiceName is the service name used inside the compose file.
// Kept in sync with the docker compose port / logs commands that query
// it by name.
const sandboxServiceName = "sandbox"

// defaultSandboxImage is the fallback used when QUARTET_SANDBOX_IMAGE is
// unset. It points at a personal Docker Hub namespace so local dev keeps
// working out of the box; any non-dev deployment must override via env.
// sandboxImage() emits a one-time WARN when the fallback is picked so the
// risk is visible in operator logs. Setting QUARTET_SANDBOX_IMAGE_STRICT=1
// upgrades this fallback to a hard panic so production deployments can't
// silently pull from the personal namespace.
const defaultSandboxImage = "fanlv/sandbox:latest"

const (
	envSandboxCPUs        = "QUARTET_SANDBOX_CPUS"
	envSandboxMemory      = "QUARTET_SANDBOX_MEMORY"
	envSandboxShmSize     = "QUARTET_SANDBOX_SHM_SIZE"
	envSandboxImageStrict = "QUARTET_SANDBOX_IMAGE_STRICT"
)

var warnDefaultImageOnce sync.Once

// sandboxImage resolves the container image for the compose template,
// allowing operators to pin a registry/tag via env without a rebuild.
// When QUARTET_SANDBOX_IMAGE_STRICT=1, falling back to the default
// personal-namespace image panics instead of warning — use this in any
// deployment that pulls images from an untrusted network.
func sandboxImage() string {
	if v := os.Getenv(envSandboxImage); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv(envSandboxImageStrict)); v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes") {
		panic(fmt.Sprintf("%s=1 and %s is not set: refusing to fall back to the personal-namespace image %q",
			envSandboxImageStrict, envSandboxImage, defaultSandboxImage))
	}
	warnDefaultImageOnce.Do(func() {
		logger.Warn("[sandbox.Manager] %s not set; falling back to %q (personal Docker Hub namespace). "+
			"Set %s to a trusted image for non-development deployments, or set %s=1 to refuse this fallback.",
			envSandboxImage, defaultSandboxImage, envSandboxImage, envSandboxImageStrict)
	})
	return defaultSandboxImage
}

func sandboxResourceValue(envKey string) string {
	return strings.TrimSpace(os.Getenv(envKey))
}

// composeFile / composeService mirror the subset of the docker-compose
// schema we actually emit. We serialise via yaml.Marshal instead of
// fmt.Sprintf so that host-controlled fields (req.HostWorkdir above all)
// cannot break out of their scalar and inject adjacent YAML keys by
// embedding colons, newlines, or quotes. Volumes use the long-form mount
// spec for the same reason: the short "host:container:mode" form is a
// single string that a colon inside the host path would repartition.
type composeFile struct {
	Services map[string]composeService `yaml:"services"`
}

type composeService struct {
	Image         string            `yaml:"image"`
	ContainerName string            `yaml:"container_name"`
	Restart       string            `yaml:"restart"`
	TTY           bool              `yaml:"tty"`
	StdinOpen     bool              `yaml:"stdin_open"`
	Labels        map[string]string `yaml:"labels"`
	Environment   map[string]string `yaml:"environment"`
	WorkingDir    string            `yaml:"working_dir"`
	Ports         []string          `yaml:"ports"`
	Volumes       []composeVolume   `yaml:"volumes"`
	Deploy        *composeDeploy    `yaml:"deploy,omitempty"`
	ShmSize       string            `yaml:"shm_size,omitempty"`
}

type composeVolume struct {
	Type     string `yaml:"type"`
	Source   string `yaml:"source"`
	Target   string `yaml:"target"`
	ReadOnly bool   `yaml:"read_only"`
}

type composeDeploy struct {
	Resources composeResources `yaml:"resources,omitempty"`
}

type composeResources struct {
	Limits composeLimits `yaml:"limits,omitempty"`
}

type composeLimits struct {
	CPUs   string `yaml:"cpus,omitempty"`
	Memory string `yaml:"memory,omitempty"`
}

// renderComposeTemplate materialises the per-workspace docker-compose
// YAML. The template mirrors what operators used to maintain by hand
// (see refactor §4.9) with the two important twists: bind mount is
// scoped to this one workspace's host workdir, and the host port for
// 8080 is "0" (docker picks).
func renderComposeTemplate(req upRequest) (string, error) {
	containerPath := containerWorkdir(req.WorkspaceID)
	resourceCPUs := sandboxResourceValue(envSandboxCPUs)
	resourceMemory := sandboxResourceValue(envSandboxMemory)
	resourceShmSize := sandboxResourceValue(envSandboxShmSize)
	if resourceCPUs == "" {
		resourceCPUs = "2"
	}
	if resourceMemory == "" {
		resourceMemory = "4g"
	}
	if resourceShmSize == "" {
		resourceShmSize = "1g"
	}
	deploy := &composeDeploy{Resources: composeResources{Limits: composeLimits{CPUs: resourceCPUs, Memory: resourceMemory}}}
	file := composeFile{
		Services: map[string]composeService{
			sandboxServiceName: {
				Image:         sandboxImage(),
				ContainerName: req.ProjectName + "_sandbox",
				Restart:       "unless-stopped",
				TTY:           true,
				StdinOpen:     true,
				Labels: map[string]string{
					"com.quartet.workspace": req.WorkspaceID,
					"com.quartet.role":      "sandbox",
				},
				Environment: map[string]string{
					"QUARTET_WORKSPACE_ID": req.WorkspaceID,
					"SANDBOX_WORKDIR":        containerPath,
				},
				WorkingDir: containerPath,
				Ports:      []string{"127.0.0.1::8080"},
				Volumes: []composeVolume{
					{
						Type:   "bind",
						Source: req.HostWorkdir,
						Target: containerPath,
					},
				},
				Deploy:  deploy,
				ShmSize: resourceShmSize,
			},
		},
	}
	out, err := yaml.Marshal(file)
	if err != nil {
		return "", fmt.Errorf("render compose template: %w", err)
	}
	return string(out), nil
}
