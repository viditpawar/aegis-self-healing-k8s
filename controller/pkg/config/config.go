// Package config loads the controller's settings from environment
// variables, falling back to defaults for anything unset.
package config

import (
	"fmt"
	"strconv"
	"time"

	"github.com/viditpawar/aegis-self-healing-k8s/controller/pkg/remediate"
)

const (
	EnvNamespace      = "AEGIS_NAMESPACE"
	EnvMaxRestarts    = "AEGIS_MAX_RESTARTS"
	EnvPendingTimeout = "AEGIS_PENDING_TIMEOUT"
	EnvLeaderElection = "AEGIS_LEADER_ELECTION"
	EnvMaxDeletions   = "AEGIS_MAX_DELETIONS_PER_MINUTE"

	// Set from the downward API; required when leader election is on.
	EnvPodName      = "POD_NAME"
	EnvPodNamespace = "POD_NAMESPACE"

	DefaultNamespace             = "aegis-workloads"
	DefaultMaxDeletionsPerMinute = 10
)

// Config is everything the controller needs to know at startup.
type Config struct {
	Namespace string
	Policy    remediate.Policy

	// MaxDeletionsPerMinute caps how many pods are deleted per minute, so a
	// bad release can't turn into a wave of deletions. 0 disables the cap.
	MaxDeletionsPerMinute int

	// LeaderElection makes replicas compete for a Lease in LeaseNamespace,
	// identifying themselves as Identity; only the leader remediates.
	LeaderElection bool
	Identity       string
	LeaseNamespace string
}

// Load builds a Config from getenv (os.Getenv in production). Invalid
// values are an error rather than silently falling back, so a typo in the
// Deployment fails loudly at startup.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		Namespace:             DefaultNamespace,
		Policy:                remediate.DefaultPolicy(),
		MaxDeletionsPerMinute: DefaultMaxDeletionsPerMinute,
	}

	if v := getenv(EnvNamespace); v != "" {
		cfg.Namespace = v
	}

	if v := getenv(EnvMaxRestarts); v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil || n < 0 {
			return Config{}, fmt.Errorf("%s must be a non-negative integer, got %q", EnvMaxRestarts, v)
		}
		cfg.Policy.MaxRestarts = int32(n)
	}

	if v := getenv(EnvPendingTimeout); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("%s must be a positive duration like 5m, got %q", EnvPendingTimeout, v)
		}
		cfg.Policy.PendingTimeout = d
	}

	if v := getenv(EnvMaxDeletions); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return Config{}, fmt.Errorf("%s must be a non-negative integer (0 disables the limit), got %q", EnvMaxDeletions, v)
		}
		cfg.MaxDeletionsPerMinute = n
	}

	if v := getenv(EnvLeaderElection); v != "" {
		on, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("%s must be true or false, got %q", EnvLeaderElection, v)
		}
		cfg.LeaderElection = on
	}
	if cfg.LeaderElection {
		cfg.Identity = getenv(EnvPodName)
		cfg.LeaseNamespace = getenv(EnvPodNamespace)
		if cfg.Identity == "" || cfg.LeaseNamespace == "" {
			return Config{}, fmt.Errorf("%s=true requires %s and %s (set them from the downward API)",
				EnvLeaderElection, EnvPodName, EnvPodNamespace)
		}
	}

	return cfg, nil
}
