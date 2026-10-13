package config

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"perps/internal/rpc"
)

const (
	DefaultDir = "/etc/perps"

	chain = "eip155:143"

	redacted = "[redacted]"

	defaultMaxInflight = 2

	defaultUpstreamFilter = "!drpc-*"
)

type Config struct {
	Chain                  string
	Exchange               string
	ExpectedImplementation string
	RPCURL                 string
	RPCUpstreamFilter      string
	RefreshInterval        time.Duration
	SlowRefreshInterval    time.Duration
	PositionPageSize       int
	MaxInflightCalls       int
	// Multicall batches the per-refresh reads through Multicall3 and false keeps one eth_call per read
	Multicall             bool
	Shocks                []float64
	LeaderboardURL        string
	LeaderboardInterval   time.Duration
	HyperSyncURL          string
	HyperSyncKey          Secret
	DeployBlock           uint64
	PositionSnapshotEvery int
	PositionRetention     time.Duration
}

// Secret keeps a credential out of logs, formatted output and JSON
type Secret string

func (s Secret) Reveal() string { return string(s) }

func (s Secret) Set() bool { return s != "" }

func (s Secret) String() string {
	if s == "" {
		return ""
	}
	return redacted
}

func (s Secret) GoString() string { return fmt.Sprintf("%q", s.String()) }

func (s Secret) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

func (s Secret) LogValue() slog.Value { return slog.StringValue(s.String()) }

type file struct {
	Chain                  string        `yaml:"chain"`
	Exchange               string        `yaml:"exchange"`
	ExpectedImplementation string        `yaml:"expected_implementation"`
	RPCURL                 string        `yaml:"rpc_url"`
	RPCUpstreamFilter      *string       `yaml:"rpc_upstream_filter"`
	RefreshInterval        time.Duration `yaml:"refresh_interval"`
	SlowRefreshInterval    time.Duration `yaml:"slow_refresh_interval"`
	PositionPageSize       int           `yaml:"position_page_size"`
	MaxInflightCalls       *int          `yaml:"max_inflight_calls"`
	Multicall              *bool         `yaml:"multicall"`
	Shocks                 []float64     `yaml:"shocks"`
	LeaderboardURL         string        `yaml:"leaderboard_url"`
	LeaderboardInterval    time.Duration `yaml:"leaderboard_interval"`
	HyperSyncURL           string        `yaml:"hypersync_url"`
	DeployBlock            uint64        `yaml:"deploy_block"`
	PositionSnapshotEvery  int           `yaml:"position_snapshot_every"`
	PositionRetention      time.Duration `yaml:"position_retention"`
}

var address = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

func Dir() string {
	if dir := os.Getenv("PERPS_CONFIG_DIR"); dir != "" {
		return dir
	}
	return DefaultDir
}

func Load(dir string) (Config, error) {
	path := filepath.Join(dir, "engine.yaml")
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	var raw file
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		return Config{}, fmt.Errorf("engine config %s: %w", path, err)
	}
	// the environment wins so compose can point at the router without editing the file
	if rpc := os.Getenv("PERPS_RPC_URL"); rpc != "" {
		raw.RPCURL = rpc
	}
	raw.applyDefaults()
	if err := raw.validate(); err != nil {
		return Config{}, fmt.Errorf("engine config %s: %w", path, err)
	}
	return Config{
		Chain:                  raw.Chain,
		Exchange:               strings.ToLower(raw.Exchange),
		ExpectedImplementation: strings.ToLower(raw.ExpectedImplementation),
		RPCURL:                 raw.RPCURL,
		RPCUpstreamFilter:      *raw.RPCUpstreamFilter,
		RefreshInterval:        raw.RefreshInterval,
		SlowRefreshInterval:    raw.SlowRefreshInterval,
		PositionPageSize:       raw.PositionPageSize,
		MaxInflightCalls:       *raw.MaxInflightCalls,
		Multicall:              *raw.Multicall,
		Shocks:                 slices.Clone(raw.Shocks),
		LeaderboardURL:         raw.LeaderboardURL,
		LeaderboardInterval:    raw.LeaderboardInterval,
		HyperSyncURL:           raw.HyperSyncURL,
		HyperSyncKey:           Secret(strings.TrimSpace(os.Getenv("ENVIO_HYPERSYNC_KEY"))),
		DeployBlock:            raw.DeployBlock,
		PositionSnapshotEvery:  raw.PositionSnapshotEvery,
		PositionRetention:      raw.PositionRetention,
	}, nil
}

func (f *file) applyDefaults() {
	if f.RPCUpstreamFilter == nil {
		f.RPCUpstreamFilter = new(defaultUpstreamFilter)
	}
	if f.MaxInflightCalls == nil {
		f.MaxInflightCalls = new(defaultMaxInflight)
	}
	if f.Multicall == nil {
		f.Multicall = new(true)
	}
}

func (f file) validate() error {
	switch {
	case f.Chain != chain:
		return fmt.Errorf("chain must be %s, got %q", chain, f.Chain)
	case !address.MatchString(f.Exchange):
		return fmt.Errorf("exchange %q is not a 0x address", f.Exchange)
	case f.ExpectedImplementation != "" && !address.MatchString(f.ExpectedImplementation):
		return fmt.Errorf("expected_implementation %q is neither empty nor a 0x address", f.ExpectedImplementation)
	case !validURL(f.RPCURL, "http", "https"):
		return errors.New("rpc_url (or PERPS_RPC_URL) must be an http or https URL without credentials")
	case !rpc.ValidUpstreamFilter(*f.RPCUpstreamFilter):
		return fmt.Errorf("rpc_upstream_filter must be empty or visible ASCII without spaces, at most %d bytes", rpc.MaxUpstreamFilter)
	case f.RefreshInterval <= 0:
		return fmt.Errorf("refresh_interval must be positive, got %s", f.RefreshInterval)
	case f.SlowRefreshInterval < f.RefreshInterval:
		return fmt.Errorf("slow_refresh_interval %s must not be shorter than refresh_interval %s", f.SlowRefreshInterval, f.RefreshInterval)
	case f.PositionPageSize < 1 || f.PositionPageSize > 500:
		return fmt.Errorf("position_page_size must be 1 to 500, got %d", f.PositionPageSize)
	case *f.MaxInflightCalls < 1 || *f.MaxInflightCalls > 64:
		return fmt.Errorf("max_inflight_calls must be 1 to 64, got %d", *f.MaxInflightCalls)
	case !validURL(f.LeaderboardURL, "http", "https"):
		return errors.New("leaderboard_url must be an http or https URL without credentials")
	case f.LeaderboardInterval <= 0:
		return fmt.Errorf("leaderboard_interval must be positive, got %s", f.LeaderboardInterval)
	case !validURL(f.HyperSyncURL, "https"):
		return errors.New("hypersync_url must be an https URL without credentials")
	case f.PositionSnapshotEvery < 1:
		return fmt.Errorf("position_snapshot_every must be at least 1, got %d", f.PositionSnapshotEvery)
	case f.PositionRetention <= 0:
		return fmt.Errorf("position_retention must be positive, got %s", f.PositionRetention)
	}
	return validShocks(f.Shocks)
}

func validShocks(shocks []float64) error {
	if len(shocks) == 0 {
		return errors.New("shocks must list at least one move")
	}
	seen := make(map[float64]bool, len(shocks))
	for _, x := range shocks {
		switch {
		case math.IsNaN(x) || x == 0 || x < -0.5 || x > 0.5:
			return fmt.Errorf("shock %v must be nonzero and within -0.5 to 0.5", x)
		case seen[x]:
			return fmt.Errorf("shock %v is listed twice", x)
		}
		seen[x] = true
	}
	return nil
}

// the value stays out of the error text because a URL can carry a token
func validURL(raw string, schemes ...string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return false
	}
	return slices.Contains(schemes, u.Scheme)
}
