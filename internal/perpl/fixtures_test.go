package perpl

import (
	"encoding/hex"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testdataDir = "../../testdata"

type callFixture struct {
	Function string            `json:"function"`
	Args     map[string]string `json:"args"`
	Block    uint64            `json:"block"`
	Request  struct {
		To   string `json:"to"`
		Data string `json:"data"`
	} `json:"request"`
	Result string `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    string `json:"data"`
	} `json:"error"`
}

type manifest struct {
	Calls []struct {
		Name     string `json:"name"`
		Function string `json:"function"`
		Error    bool   `json:"error"`
	} `json:"calls"`
}

func readJSON(t *testing.T, path string, out any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func loadManifest(t *testing.T) manifest {
	t.Helper()
	var m manifest
	readJSON(t, filepath.Join(testdataDir, "manifest.json"), &m)
	if len(m.Calls) == 0 {
		t.Fatal("manifest has no calls")
	}
	return m
}

func loadCall(t *testing.T, name string) (callFixture, []byte, any) {
	t.Helper()
	var c callFixture
	readJSON(t, filepath.Join(testdataDir, "calls", name+".json"), &c)
	var result []byte
	if c.Result != "" {
		result = mustHex(t, c.Result)
	}
	var expected any
	if c.Error == nil {
		readJSON(t, filepath.Join(testdataDir, "calls", name+".expected.json"), &expected)
	}
	return c, result, expected
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	if err != nil {
		t.Fatalf("hex %q: %v", s, err)
	}
	return b
}
