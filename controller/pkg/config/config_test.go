package config

import (
	"testing"
	"time"

	"github.com/viditpawar/aegis-self-healing-k8s/controller/pkg/remediate"
)

func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoad(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		want    Config
		wantErr bool
	}{
		{
			name: "defaults when unset",
			env:  map[string]string{},
			want: Config{Namespace: DefaultNamespace, Policy: remediate.DefaultPolicy()},
		},
		{
			name: "all overridden",
			env: map[string]string{
				EnvNamespace:      "team-a",
				EnvMaxRestarts:    "3",
				EnvPendingTimeout: "90s",
			},
			want: Config{
				Namespace: "team-a",
				Policy:    remediate.Policy{MaxRestarts: 3, PendingTimeout: 90 * time.Second},
			},
		},
		{name: "non-numeric restarts", env: map[string]string{EnvMaxRestarts: "five"}, wantErr: true},
		{name: "negative restarts", env: map[string]string{EnvMaxRestarts: "-1"}, wantErr: true},
		{name: "unitless timeout", env: map[string]string{EnvPendingTimeout: "300"}, wantErr: true},
		{name: "zero timeout", env: map[string]string{EnvPendingTimeout: "0s"}, wantErr: true},
		{
			name: "leader election with pod identity",
			env: map[string]string{
				EnvLeaderElection: "true",
				EnvPodName:        "aegis-controller-abc",
				EnvPodNamespace:   "aegis-system",
			},
			want: Config{
				Namespace:      DefaultNamespace,
				Policy:         remediate.DefaultPolicy(),
				LeaderElection: true,
				Identity:       "aegis-controller-abc",
				LeaseNamespace: "aegis-system",
			},
		},
		{
			name: "pod identity ignored when leader election is off",
			env:  map[string]string{EnvPodName: "x", EnvPodNamespace: "y"},
			want: Config{Namespace: DefaultNamespace, Policy: remediate.DefaultPolicy()},
		},
		{name: "leader election without pod name", env: map[string]string{EnvLeaderElection: "true", EnvPodNamespace: "aegis-system"}, wantErr: true},
		{name: "leader election not a bool", env: map[string]string{EnvLeaderElection: "yes please"}, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Load(envFrom(tc.env))
			if (err != nil) != tc.wantErr {
				t.Fatalf("Load() error = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Errorf("Load() = %+v, want %+v", got, tc.want)
			}
		})
	}
}
