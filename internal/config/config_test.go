package config

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func check[T comparable](t *testing.T, name string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

/* Fixtures */

const base = `chain: eip155:143
exchange: "0x34B6552d57a35a1D042CcAe1951BD1C370112a6F"
expected_implementation: ""
rpc_url: http://rpc-router:4000/sona/monad
refresh_interval: 30s
slow_refresh_interval: 10m
position_page_size: 100
max_inflight_calls: 2
shocks: [-0.2, -0.1, -0.05, -0.03, 0.03, 0.05, 0.1, 0.2]
leaderboard_url: https://app.perpl.xyz/api/v1/trading/leaderboard
leaderboard_interval: 10m
hypersync_url: https://monad.hypersync.xyz/query
deploy_block: 0
position_snapshot_every: 10
position_retention: 168h
`

// pinned to the values recorded in testdata/chain.json and testdata/deploy.json
const (
	seedImplementation = "0xa9ab97a404a0bca04d6a5b4a39995fea9e791b2a"
	seedDeployBlock    = 54773010
)

func isolate(t *testing.T) {
	t.Helper()
	for _, name := range []string{"PERPS_CONFIG_DIR", "PERPS_RPC_URL", "ENVIO_HYPERSYNC_KEY"} {
		t.Setenv(name, "")
	}
}

func writeDir(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "engine.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func swap(t *testing.T, old, repl string) string {
	t.Helper()
	if !strings.Contains(base, old) {
		t.Fatalf("fixture has no %q", old)
	}
	return strings.Replace(base, old, repl, 1)
}

/* Loading */

func TestSeedConfig(t *testing.T) {
	isolate(t)
	c, err := Load("../../config")
	if err != nil {
		t.Fatal(err)
	}
	check(t, "Chain", c.Chain, "eip155:143")
	check(t, "Exchange", c.Exchange, "0x34b6552d57a35a1d042ccae1951bd1c370112a6f")
	check(t, "ExpectedImplementation", c.ExpectedImplementation, seedImplementation)
	check(t, "RPCURL", c.RPCURL, "http://rpc-router:4000/sona/monad")
	check(t, "RefreshInterval", c.RefreshInterval, 30*time.Second)
	check(t, "SlowRefreshInterval", c.SlowRefreshInterval, 10*time.Minute)
	check(t, "PositionPageSize", c.PositionPageSize, 100)
	check(t, "MaxInflightCalls", c.MaxInflightCalls, 2)
	check(t, "Multicall", c.Multicall, true)
	check(t, "RPCUpstreamFilter", c.RPCUpstreamFilter, "!drpc-*")
	want := []float64{-0.2, -0.1, -0.05, -0.03, 0.03, 0.05, 0.1, 0.2}
	if !slices.Equal(c.Shocks, want) {
		t.Errorf("Shocks = %v, want %v", c.Shocks, want)
	}
	check(t, "LeaderboardURL", c.LeaderboardURL, "https://app.perpl.xyz/api/v1/trading/leaderboard")
	check(t, "LeaderboardInterval", c.LeaderboardInterval, 10*time.Minute)
	check(t, "HyperSyncURL", c.HyperSyncURL, "https://monad.hypersync.xyz/query")
	check(t, "HyperSyncKey set", c.HyperSyncKey.Set(), false)
	check(t, "DeployBlock", c.DeployBlock, uint64(seedDeployBlock))
	check(t, "PositionSnapshotEvery", c.PositionSnapshotEvery, 10)
	check(t, "PositionRetention", c.PositionRetention, 168*time.Hour)
}

func TestSeedFileMatchesTheTestFixture(t *testing.T) {
	isolate(t)
	seed, err := Load("../../config")
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(base, `expected_implementation: ""`, `expected_implementation: "`+seedImplementation+`"`, 1)
	body = strings.Replace(body, "deploy_block: 0", fmt.Sprintf("deploy_block: %d", seedDeployBlock), 1)
	got, err := Load(writeDir(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%+v", seed) != fmt.Sprintf("%+v", got) {
		t.Fatalf("seed %+v differs from fixture %+v", seed, got)
	}
}

func TestExpectedImplementationIsLowercased(t *testing.T) {
	isolate(t)
	c, err := Load(writeDir(t, swap(t, `expected_implementation: ""`, `expected_implementation: "0xAbCdEf0123456789aBcDeF0123456789ABCDEF01"`)))
	if err != nil {
		t.Fatal(err)
	}
	check(t, "ExpectedImplementation", c.ExpectedImplementation, "0xabcdef0123456789abcdef0123456789abcdef01")
}

func TestAcceptsEdgesOfEveryRange(t *testing.T) {
	isolate(t)
	cases := map[string]string{
		"one inflight call":   swap(t, "max_inflight_calls: 2", "max_inflight_calls: 1"),
		"most inflight calls": swap(t, "max_inflight_calls: 2", "max_inflight_calls: 64"),
		"page of one":         swap(t, "position_page_size: 100", "position_page_size: 1"),
		"equal intervals":     swap(t, "slow_refresh_interval: 10m", "slow_refresh_interval: 30s"),
		"edge shocks":         swap(t, "shocks: [-0.2, -0.1, -0.05, -0.03, 0.03, 0.05, 0.1, 0.2]", "shocks: [-0.5, 0.5]"),
		"deploy block":        swap(t, "deploy_block: 0", "deploy_block: 41000000"),
		"snapshot every one":  swap(t, "position_snapshot_every: 10", "position_snapshot_every: 1"),
	}
	for name, body := range cases {
		if _, err := Load(writeDir(t, body)); err != nil {
			t.Errorf("%s rejected: %v", name, err)
		}
	}
}

func TestMulticallSwitch(t *testing.T) {
	isolate(t)
	cases := map[string]struct {
		body string
		want bool
	}{
		"absent is on": {base, true},
		"explicit on":  {base + "multicall: true\n", true},
		"explicit off": {base + "multicall: false\n", false},
	}
	for name, c := range cases {
		got, err := Load(writeDir(t, c.body))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		check(t, name, got.Multicall, c.want)
	}
	if _, err := Load(writeDir(t, base+"multicall: maybe\n")); err == nil {
		t.Error("a non-boolean multicall was accepted")
	}
}

func TestRPCUpstreamFilter(t *testing.T) {
	isolate(t)
	cases := map[string]struct {
		body string
		want string
	}{
		"absent keeps paid upstreams out": {base, "!drpc-*"},
		"explicit default":                {base + "rpc_upstream_filter: \"!drpc-*\"\n", "!drpc-*"},
		"empty means no header":           {base + "rpc_upstream_filter: \"\"\n", ""},
		"another pattern":                 {base + "rpc_upstream_filter: \"!drpc-*|!alchemy-*\"\n", "!drpc-*|!alchemy-*"},
		"longest allowed":                 {base + "rpc_upstream_filter: " + strings.Repeat("a", 128) + "\n", strings.Repeat("a", 128)},
		"unquoted pattern":                {base + "rpc_upstream_filter: monad-*\n", "monad-*"},
	}
	for name, c := range cases {
		got, err := Load(writeDir(t, c.body))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		check(t, name, got.RPCUpstreamFilter, c.want)
	}
}

func TestRPCUpstreamFilterRejects(t *testing.T) {
	isolate(t)
	cases := map[string]string{
		"carriage return":  `"!drpc-*\r"`,
		"line feed":        `"!drpc-*\n"`,
		"header injection": `"!drpc-*\r\nX-ERPC-Use-Upstream: drpc"`,
		"tab":              `"a\tb"`,
		"space":            `"a b"`,
		"leading space":    `" !drpc-*"`,
		"nul":              `"a\u0000b"`,
		"delete":           `"a\u007fb"`,
		"non ascii":        `"d\u00e9j\u00e0"`,
		"too long":         strings.Repeat("a", 129),
		"not a string":     `[a, b]`,
	}
	for name, value := range cases {
		if c, err := Load(writeDir(t, base+"rpc_upstream_filter: "+value+"\n")); err == nil {
			t.Errorf("%s accepted: %+v", name, c)
		} else if strings.Contains(err.Error(), "X-ERPC-Use-Upstream") {
			t.Errorf("%s echoes the value: %v", name, err)
		}
	}
}

func TestMaxInflightCallsDefaultsToTwo(t *testing.T) {
	isolate(t)
	body := strings.Replace(base, "max_inflight_calls: 2\n", "", 1)
	c, err := Load(writeDir(t, body))
	if err != nil {
		t.Fatal(err)
	}
	check(t, "MaxInflightCalls", c.MaxInflightCalls, 2)
}

func TestLoadRejects(t *testing.T) {
	isolate(t)
	cases := map[string]string{
		"another chain":                        swap(t, "chain: eip155:143", "chain: eip155:1"),
		"no chain":                             swap(t, "chain: eip155:143\n", ""),
		"unknown key":                          base + "cutoff: 3\n",
		"exchange too short":                   swap(t, `"0x34B6552d57a35a1D042CcAe1951BD1C370112a6F"`, `"0x34B6552d"`),
		"exchange without prefix":              swap(t, `"0x34B6552d57a35a1D042CcAe1951BD1C370112a6F"`, `"34B6552d57a35a1D042CcAe1951BD1C370112a6F00"`),
		"exchange not hex":                     swap(t, `"0x34B6552d57a35a1D042CcAe1951BD1C370112a6F"`, `"0xZZB6552d57a35a1D042CcAe1951BD1C370112a6F"`),
		"no exchange":                          swap(t, "exchange: \"0x34B6552d57a35a1D042CcAe1951BD1C370112a6F\"\n", ""),
		"implementation not address":           swap(t, `expected_implementation: ""`, `expected_implementation: "0x12"`),
		"empty rpc url":                        swap(t, "rpc_url: http://rpc-router:4000/sona/monad", `rpc_url: ""`),
		"rpc url scheme":                       swap(t, "http://rpc-router:4000/sona/monad", "ftp://rpc-router:4000/sona/monad"),
		"rpc url without host":                 swap(t, "http://rpc-router:4000/sona/monad", "http:///sona/monad"),
		"rpc url with credentials":             swap(t, "http://rpc-router:4000/sona/monad", "http://user:pw@rpc-router:4000/sona/monad"),
		"rpc url relative":                     swap(t, "http://rpc-router:4000/sona/monad", "/sona/monad"),
		"refresh not a duration":               swap(t, "refresh_interval: 30s", "refresh_interval: soon"),
		"refresh is a bare number":             swap(t, "refresh_interval: 30s", "refresh_interval: 30"),
		"refresh zero":                         swap(t, "refresh_interval: 30s", "refresh_interval: 0s"),
		"refresh negative":                     swap(t, "refresh_interval: 30s", "refresh_interval: -5s"),
		"slow shorter than refresh":            swap(t, "slow_refresh_interval: 10m", "slow_refresh_interval: 10s"),
		"page size zero":                       swap(t, "position_page_size: 100", "position_page_size: 0"),
		"page size negative":                   swap(t, "position_page_size: 100", "position_page_size: -1"),
		"page size too large":                  swap(t, "position_page_size: 100", "position_page_size: 501"),
		"no inflight calls":                    swap(t, "max_inflight_calls: 2", "max_inflight_calls: 0"),
		"too many inflight calls":              swap(t, "max_inflight_calls: 2", "max_inflight_calls: 65"),
		"no shocks":                            swap(t, "shocks: [-0.2, -0.1, -0.05, -0.03, 0.03, 0.05, 0.1, 0.2]", "shocks: []"),
		"shocks missing":                       swap(t, "shocks: [-0.2, -0.1, -0.05, -0.03, 0.03, 0.05, 0.1, 0.2]\n", ""),
		"zero shock":                           swap(t, "-0.03, 0.03", "-0.03, 0"),
		"shock below range":                    swap(t, "-0.2, -0.1", "-0.51, -0.1"),
		"shock above range":                    swap(t, "0.1, 0.2]", "0.1, 0.51]"),
		"duplicate shock":                      swap(t, "0.1, 0.2]", "0.1, 0.1]"),
		"nan shock":                            swap(t, "0.1, 0.2]", "0.1, .nan]"),
		"infinite shock":                       swap(t, "0.1, 0.2]", "0.1, .inf]"),
		"leaderboard url scheme":               swap(t, "https://app.perpl.xyz/api/v1/trading/leaderboard", "file:///etc/passwd"),
		"leaderboard url empty":                swap(t, "leaderboard_url: https://app.perpl.xyz/api/v1/trading/leaderboard", `leaderboard_url: ""`),
		"leaderboard interval zero":            swap(t, "leaderboard_interval: 10m", "leaderboard_interval: 0s"),
		"hypersync over plain http":            swap(t, "https://monad.hypersync.xyz/query", "http://monad.hypersync.xyz/query"),
		"hypersync url empty":                  swap(t, "hypersync_url: https://monad.hypersync.xyz/query", `hypersync_url: ""`),
		"negative deploy block":                swap(t, "deploy_block: 0", "deploy_block: -1"),
		"snapshot every zero":                  swap(t, "position_snapshot_every: 10", "position_snapshot_every: 0"),
		"retention zero":                       swap(t, "position_retention: 168h", "position_retention: 0s"),
		"retention not a duration":             swap(t, "position_retention: 168h", "position_retention: week"),
		"not yaml":                             "chain: [",
		"empty file":                           "",
		"duplicate key in the mapping":         base + "refresh_interval: 40s\n",
		"map default key that nothing reads":   base + "bucket_bps: 25\n",
		"range default key that nothing reads": base + "range_pct: 20\n",
	}
	for name, body := range cases {
		if c, err := Load(writeDir(t, body)); err == nil {
			t.Errorf("%s accepted: %+v", name, c)
		}
	}
}

func TestLoadRejectsMissingFile(t *testing.T) {
	isolate(t)
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("missing engine.yaml accepted")
	}
}

/* Environment */

func TestDir(t *testing.T) {
	isolate(t)
	check(t, "default", Dir(), "/etc/perps")
	t.Setenv("PERPS_CONFIG_DIR", "/srv/perps")
	check(t, "override", Dir(), "/srv/perps")
}

func TestRPCURLEnvOverridesTheFile(t *testing.T) {
	isolate(t)
	t.Setenv("PERPS_RPC_URL", "http://127.0.0.1:4000/sona/monad")
	c, err := Load(writeDir(t, base))
	if err != nil {
		t.Fatal(err)
	}
	check(t, "RPCURL", c.RPCURL, "http://127.0.0.1:4000/sona/monad")
}

func TestRPCURLEnvSuppliesAnEmptyFileValue(t *testing.T) {
	isolate(t)
	t.Setenv("PERPS_RPC_URL", "http://127.0.0.1:4000/sona/monad")
	c, err := Load(writeDir(t, swap(t, "rpc_url: http://rpc-router:4000/sona/monad", `rpc_url: ""`)))
	if err != nil {
		t.Fatal(err)
	}
	check(t, "RPCURL", c.RPCURL, "http://127.0.0.1:4000/sona/monad")
}

func TestBadRPCURLEnvIsRejectedWithoutEchoingIt(t *testing.T) {
	isolate(t)
	const leaky = "ftp://router.example/token-abc123"
	t.Setenv("PERPS_RPC_URL", leaky)
	_, err := Load(writeDir(t, base))
	if err == nil {
		t.Fatal("bad PERPS_RPC_URL accepted")
	}
	if strings.Contains(err.Error(), "abc123") || strings.Contains(err.Error(), leaky) {
		t.Fatalf("error echoes the value: %v", err)
	}
}

func TestHyperSyncKeyComesFromTheEnvironmentOnly(t *testing.T) {
	isolate(t)
	t.Setenv("ENVIO_HYPERSYNC_KEY", "  key-0123456789  \n")
	c, err := Load(writeDir(t, base))
	if err != nil {
		t.Fatal(err)
	}
	check(t, "Set", c.HyperSyncKey.Set(), true)
	check(t, "Reveal", c.HyperSyncKey.Reveal(), "key-0123456789")
	if _, err := Load(writeDir(t, base+"hypersync_key: abc\n")); err == nil {
		t.Fatal("hypersync_key in the file accepted")
	}
}

/* Secrets stay out of output */

func TestHyperSyncKeyIsRedactedEverywhere(t *testing.T) {
	isolate(t)
	const key = "key-0123456789"
	t.Setenv("ENVIO_HYPERSYNC_KEY", key)
	c, err := Load(writeDir(t, base))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		out = append(out, fmt.Sprintf(verb, c), fmt.Sprintf(verb, c.HyperSyncKey), fmt.Sprintf(verb, &c))
	}
	asJSON, err := json.Marshal(struct{ Key Secret }{c.HyperSyncKey})
	if err != nil {
		t.Fatal(err)
	}
	out = append(out, string(asJSON))
	var logs bytes.Buffer
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("loaded", "key", c.HyperSyncKey, "config", c)
	out = append(out, logs.String())
	for _, s := range out {
		if strings.Contains(s, key) {
			t.Errorf("secret leaked in %q", s)
		}
	}
	if !strings.Contains(fmt.Sprintf("%v", c.HyperSyncKey), "redacted") {
		t.Errorf("unset marker missing: %v", c.HyperSyncKey)
	}
}

func TestUnsetKeyPrintsEmpty(t *testing.T) {
	var s Secret
	check(t, "String", s.String(), "")
	check(t, "Set", s.Set(), false)
}
