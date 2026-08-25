package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// DefaultBaseURL is the hosted instance. A self-hoster overrides it with
// PUNCHCARD_URL or `punchcard login --url`.
const DefaultBaseURL = "https://punchcard.cobanov.run"

// Config is what the CLI remembers between runs: where the server is and how to
// prove who you are. Nothing else — a cache would be one more thing to go stale.
type Config struct {
	BaseURL string `json:"base_url"`
	Token   string `json:"token"`
	Login   string `json:"login,omitempty"`
}

// ConfigPath is where the config lives, honouring XDG_CONFIG_HOME.
func ConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("cannot find a config directory: %w", err)
	}
	return filepath.Join(dir, "punchcard", "config.json"), nil
}

// LoadConfig reads the config. A missing file means "not signed in", which is a
// state with an obvious next step rather than an error to explain.
func LoadConfig(path string) (Config, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- path is the CLI's own config location
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, ErrNotLoggedIn
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return Config{}, fmt.Errorf("config at %s is unreadable: %w", path, err)
	}
	if c.Token == "" {
		return Config{}, ErrNotLoggedIn
	}
	if c.BaseURL == "" {
		c.BaseURL = DefaultBaseURL
	}
	return c, nil
}

// SaveConfig writes the config with owner-only permissions.
//
// The file holds a bearer token: on a shared machine, a world-readable token is
// a token that did not need stealing. The mode is set explicitly rather than
// left to the umask, because a permissive umask is exactly the environment
// where this matters.
func SaveConfig(path string, c Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	payload, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(payload, '\n'), 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	// WriteFile only applies the mode when it creates the file, so an existing
	// file keeps whatever it had. Set it again.
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("secure config: %w", err)
	}
	return nil
}

// ClearConfig removes the stored token.
func ClearConfig(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove config: %w", err)
	}
	return nil
}
