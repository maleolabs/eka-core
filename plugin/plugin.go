// Package plugin defines the EKA plugin contract (v1): a stable,
// versionable, executable-based integration point for extensions such as
// eka-mcp.
//
// The contract keeps consumers (the CLI) decoupled from any plugin's
// implementation. The CLI depends only on this package — never on eka-mcp
// or any other plugin. A plugin is an executable named "eka-<name>" (e.g.
// "eka-mcp") discoverable on PATH or under the EKA plugin directory; the
// CLI talks to it through two machine-readable subcommands:
//
//	eka-<name> manifest --json
//	eka-<name> install <kind> --dir <dir> [--dry-run] --json
//
// "manifest" reports what the plugin provides (name, version, installable
// artifact families), "install" delegates an artifact-family installation
// into an agent configuration directory. The JSON output is the contract:
// it is deterministic, schema-stable, and versioned by ContractVersion.
//
// Plugins import these types to implement their executable side
// (see eka-mcp); the CLI imports them to drive plugins. Neither side
// imports the other's internal packages — this package is the shared
// contract between them.
package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// ContractVersion is the machine-readable contract version. Consumers and
// providers negotiate against it; a mismatch is a refusal, never a silent
// misinterpretation.
const ContractVersion = "v1"

// Manifest is the machine-readable self-description a plugin executable
// emits for "manifest --json". It is the single source of truth the CLI
// uses to know what a plugin provides.
type Manifest struct {
	// Contract is the contract version the manifest is written against
	// (must equal ContractVersion).
	Contract string `json:"contract"`
	// Name is the stable plugin identity (e.g. "mcp").
	Name string `json:"name"`
	// Version is the plugin semantic version.
	Version string `json:"version"`
	// Description is a human-readable one-line summary.
	Description string `json:"description"`
	// Artifacts lists the installable artifact families.
	Artifacts []Artifact `json:"artifacts"`
	// Capabilities declares what the plugin can do: "install" (artifact
	// installation) and/or "mcp" (MCP server). Absent (nil) for legacy
	// plugins that predate the field.
	Capabilities []string `json:"capabilities"`
	// Source is the canonical source repository of the plugin
	// (e.g. "github.com/maleolabs/eka-mcp"). Empty for legacy plugins.
	Source string `json:"source"`
}

// Artifact is one installable family the plugin can install into an agent
// configuration directory.
type Artifact struct {
	// Kind is the family name: "skills", "commands", "tools", …
	Kind string `json:"kind"`
	// Entries are the artifact names within the family (skill directory
	// names, command file names, …).
	Entries []string `json:"entries"`
}

// InstallOptions is the request for a plugin "install <kind>" delegation.
type InstallOptions struct {
	Kind   string
	Dir    string
	DryRun bool
}

// InstallResult is the machine-readable result of an install delegation.
type InstallResult struct {
	// Installed are the artifact names installed (or that would be).
	Installed []string `json:"installed"`
	// Version is the plugin version that served the install.
	Version string `json:"version"`
}

// Plugin is a discovered plugin executable. Its methods invoke the
// executable subcommands and parse their JSON output.
type Plugin struct {
	// Exe is the absolute path of the plugin executable.
	Exe string
}

// pluginDirEnv is the environment variable overriding the plugin
// directory; pluginDirDefault is the fallback under the EKA home.
const (
	pluginDirEnv     = "EKA_PLUGIN_DIR"
	pluginDirDefault = ".eka/plugins"
)

// DefaultPluginPaths returns the ordered list of directories searched for
// plugin executables: $EKA_PLUGIN_DIR, then ~/.eka/plugins. PATH search is
// applied by Discover separately via exec.LookPath.
func DefaultPluginPaths(home string) []string {
	var paths []string
	if d := os.Getenv(pluginDirEnv); d != "" {
		paths = append(paths, d)
	}
	if home != "" {
		paths = append(paths, filepath.Join(home, pluginDirDefault))
	}
	return paths
}

// PluginDir returns the single directory plugins are installed into:
// $EKA_PLUGIN_DIR when set, else <home>/.eka/plugins. It is the first
// entry of DefaultPluginPaths — the install target of `eka plugin
// install`.
//
// Invariant: home is the user home directory (os.UserHomeDir), which is
// never empty in the install path; PluginDir returns "" only when
// neither $EKA_PLUGIN_DIR nor a home is available. Callers must treat
// "" as a refusal — never fall back to the current directory.
func PluginDir(home string) string {
	paths := DefaultPluginPaths(home)
	if len(paths) == 0 {
		return ""
	}
	return paths[0]
}

// Discover finds plugin executables: any "eka-*" executable on PATH
// (excluding the CLI itself) plus any "eka-*" executable in the plugin
// directories. Duplicate names collapse to the first discovered path.
// A plugin whose manifest cannot be read is skipped only when a runnable
// candidate of the same name also failed to produce a manifest; otherwise
// the error is returned so a broken plugin is visible, not silent.
func Discover(home string) ([]Plugin, error) {
	seen := map[string]bool{}
	var plugins []Plugin
	add := func(exe string) {
		name := pluginName(exe)
		if name == "" || name == "eka" || seen[name] {
			return
		}
		seen[name] = true
		plugins = append(plugins, Plugin{Exe: exe})
	}
	// PATH search.
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if strings.HasPrefix(e.Name(), "eka-") {
				add(filepath.Join(dir, e.Name()))
			}
		}
	}
	// Plugin directories.
	for _, dir := range DefaultPluginPaths(home) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if strings.HasPrefix(e.Name(), "eka-") {
				add(filepath.Join(dir, e.Name()))
			}
		}
	}
	sort.Slice(plugins, func(i, j int) bool { return pluginName(plugins[i].Exe) < pluginName(plugins[j].Exe) })
	return plugins, nil
}

// pluginName extracts the stable plugin name from an executable path:
// the basename with the leading "eka-" prefix dropped. A trailing
// ".old" (the preserved-old-binary marker of the CLI's atomic
// replace) is debris, never a plugin: an "eka-<name>.old" leftover of
// a completed update must not be discovered.
func pluginName(exe string) string {
	base := filepath.Base(exe)
	if !strings.HasPrefix(base, "eka-") {
		return ""
	}
	name := strings.TrimPrefix(base, "eka-")
	if strings.HasSuffix(name, ".old") {
		return ""
	}
	return name
}

// Manifest runs "manifest --json" and parses the result. The plugin
// process is not bounded by this method (the unbounded form — the
// normal contract path); callers that need a deadline or output cap,
// such as the install smoke check, use ManifestContext.
func (p Plugin) Manifest() (Manifest, error) {
	return p.ManifestContext(context.Background())
}

// ManifestContext runs "manifest --json" bounded by ctx (e.g. a
// timeout) and parses the result. The plugin's stdout is capped at
// maxPluginOutputSize; a larger output refuses (fail-closed).
func (p Plugin) ManifestContext(ctx context.Context) (Manifest, error) {
	out, err := p.runContext(ctx, "manifest", "--json")
	if err != nil {
		return Manifest{}, fmt.Errorf("plugin %q manifest failed: %w", pluginName(p.Exe), err)
	}
	var m Manifest
	if err := json.Unmarshal(out, &m); err != nil {
		return Manifest{}, fmt.Errorf("plugin %q manifest is not valid JSON: %w", pluginName(p.Exe), err)
	}
	if m.Contract != "" && m.Contract != ContractVersion {
		return Manifest{}, fmt.Errorf("plugin %q contract %q is not supported (want %q)", pluginName(p.Exe), m.Contract, ContractVersion)
	}
	return m, nil
}

// Install runs "install <kind> --dir <dir> [--dry-run] --json" and parses
// the result.
func (p Plugin) Install(opts InstallOptions) (InstallResult, error) {
	args := []string{"install", opts.Kind, "--dir", opts.Dir, "--json"}
	if opts.DryRun {
		args = append(args, "--dry-run")
	}
	out, err := p.run(args...)
	if err != nil {
		return InstallResult{}, fmt.Errorf("plugin %q install %s failed: %w", pluginName(p.Exe), opts.Kind, err)
	}
	var r InstallResult
	if err := json.Unmarshal(out, &r); err != nil {
		return InstallResult{}, fmt.Errorf("plugin %q install %s result is not valid JSON: %w", pluginName(p.Exe), opts.Kind, err)
	}
	return r, nil
}

// run executes the plugin executable with the given arguments without a
// deadline (see runContext).
func (p Plugin) run(args ...string) ([]byte, error) {
	return p.runContext(context.Background(), args...)
}

// maxPluginOutputSize caps the stdout a plugin may write for one
// invocation (a manifest or install result is a few KiB; anything
// larger is a broken or hostile plugin). It bounds the memory of the
// contract subprocesses — a spewing plugin is refused, never buffered
// into exhaustion.
const maxPluginOutputSize = 1 << 20 // 1 MiB

// runContext executes the plugin executable with the given arguments,
// bounded by ctx and by maxPluginOutputSize on stdout: it returns the
// stdout bytes on success (stderr is surfaced on failure). A plugin
// that hangs past ctx's deadline, or writes more than
// maxPluginOutputSize, is refused — a hung or spewing plugin must not
// wedge the CLI or exhaust memory. The subprocess runs under the
// minimal environment whitelist (pluginEnv): a plugin must never see
// the CLI user's secrets.
func (p Plugin) runContext(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, p.Exe, args...)
	cmd.Env = pluginEnv()
	var stdout limitedBuffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if stdout.overflow {
			return nil, fmt.Errorf("plugin output exceeds %d bytes", maxPluginOutputSize)
		}
		return nil, errors.New(strings.TrimSpace(stderr.String()))
	}
	if stdout.overflow {
		return nil, fmt.Errorf("plugin output exceeds %d bytes", maxPluginOutputSize)
	}
	return stdout.buf.Bytes(), nil
}

// pluginEnv is the minimal environment whitelist the CLI grants a
// plugin subprocess — an explicit allow-list, never a denylist. The
// plugin contract (manifest/install subcommands with explicit --dir
// arguments) needs PATH (locating tools), HOME (user configuration),
// EKA_PLUGIN_DIR (the CLI-managed plugin directory) and, on Windows,
// SystemRoot (DLL resolution). Everything else — notably credentials
// such as GH_TOKEN, SSH_AUTH_SOCK and cloud-provider variables — is
// deliberately NOT inherited: a third-party binary is executed for its
// manifest BEFORE the consent decision, and it must not be able to
// read the user's secrets from the environment.
func pluginEnv() []string {
	env := []string{"PATH=" + os.Getenv("PATH")}
	for _, key := range []string{"HOME", "EKA_PLUGIN_DIR", "SystemRoot"} {
		if v := os.Getenv(key); v != "" {
			env = append(env, key+"="+v)
		}
	}
	return env
}

// limitedBuffer writes into an internal buffer up to
// maxPluginOutputSize bytes; further writes are counted as overflow and
// refused (the plugin is killed via SIGPIPE when the pipe drains), so a
// spewing plugin cannot exhaust memory.
type limitedBuffer struct {
	buf      bytes.Buffer
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.overflow {
		return len(p), nil
	}
	remaining := maxPluginOutputSize - b.buf.Len()
	if remaining <= 0 {
		b.overflow = true
		return len(p), nil
	}
	if len(p) > remaining {
		b.overflow = true
		b.buf.Write(p[:remaining])
		return len(p), nil
	}
	b.buf.Write(p)
	return len(p), nil
}
