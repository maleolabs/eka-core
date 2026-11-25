package plugin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The plugin package tests are hermetic: a fake plugin executable (a
// shell script implementing the contract) is written into a temporary
// bin directory, and PATH/EKA_PLUGIN_DIR are pinned to controlled
// directories so no ambient eka-* executable interferes.

const fakeExe = `#!/bin/sh
case "$1" in
  manifest)
    # The contract is "manifest --json"; a bare "manifest" must refuse
    # exactly like the real binaries do (usage on stderr, exit 1), so
    # the tests catch a CLI-side contract drift.
    if [ "$2" != "--json" ]; then
      echo "usage: eka-mcp manifest --json" >&2
      exit 1
    fi
    cat <<'EOF'
{"contract":"v1","name":"mcp","version":"2.3.4","description":"fake","artifacts":[{"kind":"skills","entries":["eka-a","eka-b"]}],"capabilities":["install","mcp"],"source":"github.com/maleolabs/eka-mcp"}
EOF
    ;;
    install)
    shift 2
    while [ "$#" -gt 0 ]; do
      case "$1" in
        --dry-run) dry=1; shift ;;
        *) shift ;;
      esac
    done
    printf '%s' '{"installed":["eka-a","eka-b"],"version":"2.3.4"}'
    ;;
esac
`

// writeFakeExe writes the fake plugin executable into bin, pins
// EKA_PLUGIN_DIR to it, and prepends bin to PATH so discovery finds
// the fake before anything ambient, returning the plugin's path.
func writeFakeExe(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	exe := filepath.Join(bin, "eka-mcp")
	if err := os.WriteFile(exe, []byte(fakeExe), 0o755); err != nil {
		t.Fatalf("write fake plugin: %v", err)
	}
	t.Setenv("EKA_PLUGIN_DIR", bin)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return exe
}

func TestDiscoverFindsPluginInPluginDir(t *testing.T) {
	writeFakeExe(t)
	plugins, err := Discover("/nonexistent-home")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(plugins) != 1 {
		t.Fatalf("plugins = %d, want 1: %+v", len(plugins), plugins)
	}
	if got := filepath.Base(plugins[0].Exe); got != "eka-mcp" {
		t.Errorf("exe = %q, want eka-mcp", got)
	}
}

func TestDiscoverSkipsNonPluginNames(t *testing.T) {
	bin := t.TempDir()
	// An "eka" binary (the CLI itself) and a non-eka binary must be
	// skipped; only eka-* siblings count.
	for _, name := range []string{"eka", "not-a-plugin", "eka-mcp"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	t.Setenv("EKA_PLUGIN_DIR", bin)
	t.Setenv("PATH", t.TempDir())
	plugins, err := Discover("")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(plugins) != 1 || filepath.Base(plugins[0].Exe) != "eka-mcp" {
		t.Fatalf("plugins = %+v, want only eka-mcp", plugins)
	}
}

func TestDiscoverEmpty(t *testing.T) {
	t.Setenv("EKA_PLUGIN_DIR", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	plugins, err := Discover("")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(plugins) != 0 {
		t.Errorf("plugins = %+v, want none", plugins)
	}
}

// TestDiscoverSkipsOldMarkers: an "eka-<name>.old" leftover of the
// CLI's atomic replace (the update command preserves the old binary
// as <target>.old) is debris, never a plugin — it must not be
// discovered.
func TestDiscoverSkipsOldMarkers(t *testing.T) {
	bin := t.TempDir()
	for _, name := range []string{"eka-mcp", "eka-mcp.old", "eka-helper.old"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	t.Setenv("EKA_PLUGIN_DIR", bin)
	t.Setenv("PATH", t.TempDir())
	plugins, err := Discover("")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(plugins) != 1 || filepath.Base(plugins[0].Exe) != "eka-mcp" {
		t.Fatalf("plugins = %+v, want only eka-mcp (the .old markers are debris)", plugins)
	}
}

// TestManifestOutputTooLargeRefused: a plugin that writes more than
// maxPluginOutputSize to stdout is refused — a spewing plugin must not
// exhaust memory (bounded read, fail-closed).
func TestManifestOutputTooLargeRefused(t *testing.T) {
	bin := t.TempDir()
	// 64 × 64 KiB of 'x' (4 MiB) on stdout — well past the 1 MiB cap.
	script := `#!/bin/sh
dd if=/dev/zero bs=65536 count=64 2>/dev/null | tr '\000' 'x'
`
	exe := filepath.Join(bin, "eka-mcp")
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := (Plugin{Exe: exe}).Manifest()
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("an oversized manifest output must refuse, got err = %v", err)
	}
}

// TestManifestContextTimeout: a plugin that hangs past the context
// deadline is killed and the deadline error is surfaced — a hung plugin
// must not wedge the caller.
func TestManifestContextTimeout(t *testing.T) {
	bin := t.TempDir()
	script := `#!/bin/sh
while true; do :; done
`
	exe := filepath.Join(bin, "eka-mcp")
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := (Plugin{Exe: exe}).ManifestContext(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a hung plugin must surface the deadline error, got err = %v", err)
	}
}

// TestPluginDir: the install-target accessor — $EKA_PLUGIN_DIR wins,
// the home fallback is <home>/.eka/plugins, and "" (the documented
// refusal case — never install into the current directory) is returned
// only when neither is available.
func TestPluginDir(t *testing.T) {
	t.Setenv("EKA_PLUGIN_DIR", "")
	if got := PluginDir(""); got != "" {
		t.Errorf("PluginDir(\"\") = %q, want \"\" (callers must refuse)", got)
	}
	if got, want := PluginDir("/home/user"), filepath.Join("/home/user", ".eka", "plugins"); got != want {
		t.Errorf("PluginDir(\"/home/user\") = %q, want %q", got, want)
	}
	t.Setenv("EKA_PLUGIN_DIR", "/custom/plugins")
	if got := PluginDir(""); got != "/custom/plugins" {
		t.Errorf("PluginDir with EKA_PLUGIN_DIR = %q, want /custom/plugins", got)
	}
}

func TestManifestParsesJSON(t *testing.T) {
	writeFakeExe(t)
	plugins, err := Discover("")
	if err != nil || len(plugins) != 1 {
		t.Fatalf("Discover: plugins = %+v, err = %v", plugins, err)
	}
	m, err := plugins[0].Manifest()
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if m.Contract != ContractVersion || m.Name != "mcp" || m.Version != "2.3.4" {
		t.Errorf("manifest = %+v", m)
	}
	if len(m.Artifacts) != 1 || m.Artifacts[0].Kind != "skills" {
		t.Errorf("artifacts = %+v", m.Artifacts)
	}
	if len(m.Artifacts[0].Entries) != 2 || m.Artifacts[0].Entries[0] != "eka-a" {
		t.Errorf("entries = %+v", m.Artifacts[0].Entries)
	}
	// The eka-mcp manifest shape (cross-repo contract pin): capabilities
	// and source must parse.
	if len(m.Capabilities) != 2 || m.Capabilities[0] != "install" || m.Capabilities[1] != "mcp" {
		t.Errorf("capabilities = %+v, want [install mcp]", m.Capabilities)
	}
	if m.Source != "github.com/maleolabs/eka-mcp" {
		t.Errorf("source = %q, want github.com/maleolabs/eka-mcp", m.Source)
	}
}

// TestManifestLegacyWithoutCapabilities: a pre-contract-pin manifest
// (no capabilities/source fields) still parses — the fields are
// optional and absent for legacy plugins.
func TestManifestLegacyWithoutCapabilities(t *testing.T) {
	bin := t.TempDir()
	script := `#!/bin/sh
printf '%s' '{"contract":"v1","name":"legacy","version":"1.0.0","description":"old","artifacts":[]}'
`
	exe := filepath.Join(bin, "eka-legacy")
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	m, err := (Plugin{Exe: exe}).Manifest()
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if m.Name != "legacy" || m.Version != "1.0.0" {
		t.Errorf("manifest = %+v", m)
	}
	if m.Capabilities != nil || m.Source != "" {
		t.Errorf("legacy manifest must carry empty capabilities/source, got %+v / %q", m.Capabilities, m.Source)
	}
}

// TestManifestUnknownFieldsIgnored: unknown fields from NEWER manifests
// must not break parsing (standard encoding/json behavior) — the CLI
// stays forward-compatible with the contract.
func TestManifestUnknownFieldsIgnored(t *testing.T) {
	bin := t.TempDir()
	script := `#!/bin/sh
printf '%s' '{"contract":"v1","name":"mcp","version":"2.3.4","futureField":{"nested":1},"capabilities":["install","mcp"],"source":"github.com/maleolabs/eka-mcp","another":"x"}'
`
	exe := filepath.Join(bin, "eka-mcp")
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	m, err := (Plugin{Exe: exe}).Manifest()
	if err != nil {
		t.Fatalf("Manifest must ignore unknown fields: %v", err)
	}
	if m.Name != "mcp" || len(m.Capabilities) != 2 || m.Source != "github.com/maleolabs/eka-mcp" {
		t.Errorf("manifest = %+v", m)
	}
}

func TestManifestContractMismatchRefused(t *testing.T) {
	bin := t.TempDir()
	script := `#!/bin/sh
printf '%s' '{"contract":"v9","name":"mcp","version":"1.0.0"}'
`
	exe := filepath.Join(bin, "eka-mcp")
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("EKA_PLUGIN_DIR", bin)
	t.Setenv("PATH", t.TempDir())
	p := Plugin{Exe: exe}
	if _, err := p.Manifest(); err == nil || !strings.Contains(err.Error(), "contract") {
		t.Errorf("contract mismatch must refuse, got err = %v", err)
	}
}

func TestInstallRunsPluginAndParsesResult(t *testing.T) {
	exe := writeFakeExe(t)
	p := Plugin{Exe: exe}
	res, err := p.Install(InstallOptions{Kind: "skills", Dir: "/target/dir"})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if res.Version != "2.3.4" || len(res.Installed) != 2 {
		t.Errorf("result = %+v", res)
	}
}

func TestInstallFailureSurfacesStderr(t *testing.T) {
	bin := t.TempDir()
	script := `#!/bin/sh
echo "plugin exploded" >&2
exit 3
`
	exe := filepath.Join(bin, "eka-mcp")
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	p := Plugin{Exe: exe}
	_, err := p.Install(InstallOptions{Kind: "skills", Dir: "/x"})
	if err == nil || !strings.Contains(err.Error(), "plugin exploded") {
		t.Errorf("failure must surface stderr, got err = %v", err)
	}
}
