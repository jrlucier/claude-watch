// Package config loads and persists user settings.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Config is the on-disk user-settings document.
type Config struct {
	APIRefreshSeconds   int    `toml:"api_refresh_seconds"`
	JSONLRefreshSeconds int    `toml:"jsonl_refresh_seconds"`
	LabelMode           string `toml:"label_mode"` // "5h" or "both"
	ProxyURL            string `toml:"proxy_url"`
	NotifyThresholds    []int  `toml:"notify_thresholds"`
}

// Default returns a Config populated with the documented defaults.
func Default() Config {
	return Config{
		APIRefreshSeconds:   300, // 5 min: Anthropic rate-limits aggressive polling
		JSONLRefreshSeconds: 30,
		LabelMode:           "5h",
		ProxyURL:            "",
		NotifyThresholds:    []int{80, 95},
	}
}

// MinAPIRefreshSeconds is the floor for API polling. Anything more aggressive
// than this triggers 429 rate-limit errors in practice.
const MinAPIRefreshSeconds = 300

// Path returns the canonical config path: $XDG_CONFIG_HOME/claude-watch/config.toml.
func Path() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "claude-watch", "config.toml"), nil
}

// Load reads the config file, falling back to defaults for any missing fields.
// A missing file is not an error — Default() is returned.
func Load() (Config, error) {
	p, err := Path()
	if err != nil {
		return Config{}, err
	}
	cfg := Default()
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("read %s: %w", p, err)
	}
	if _, err := toml.Decode(string(b), &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", p, err)
	}
	applyDefaults(&cfg)
	return cfg, nil
}

// applyDefaults fills zero values with the documented defaults so a half-edited
// config doesn't end up with `api_refresh_seconds = 0` and a busy-loop daemon.
func applyDefaults(c *Config) {
	d := Default()
	if c.APIRefreshSeconds < MinAPIRefreshSeconds {
		c.APIRefreshSeconds = MinAPIRefreshSeconds
	}
	if c.JSONLRefreshSeconds <= 0 {
		c.JSONLRefreshSeconds = d.JSONLRefreshSeconds
	}
	if c.LabelMode == "" {
		c.LabelMode = d.LabelMode
	}
	if c.NotifyThresholds == nil {
		c.NotifyThresholds = d.NotifyThresholds
	}
}

// Save writes cfg to the canonical path with an atomic rename.
func Save(cfg Config) error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(p), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".config-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	enc := toml.NewEncoder(tmp)
	if err := enc.Encode(cfg); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}
