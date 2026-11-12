package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Config is what signing in leaves on this machine: the engine and the
// personal token (a credential: the file is the user's only).
type Config struct {
	URL     string `json:"url"`
	Token   string `json:"token"`
	TokenID string `json:"token_id,omitempty"` // to revoke it at logout; "" for a pasted token
}

func configPath(env Env) (string, error) {
	if env.ConfigDir == "" {
		return "", errors.New("no configuration directory on this machine")
	}
	return filepath.Join(env.ConfigDir, "apothecary", "config.json"), nil
}

// loadConfig reads the saved sign-in; APOTHECARY_URL and APOTHECARY_TOKEN
// override it (a script, a CI job).
func loadConfig(env Env) (Config, error) {
	var cfg Config
	path, err := configPath(env)
	if err == nil {
		data, rerr := os.ReadFile(path)
		switch {
		case rerr == nil:
			if jerr := json.Unmarshal(data, &cfg); jerr != nil {
				return cfg, fmt.Errorf("%s: %w", path, jerr)
			}
		case !errors.Is(rerr, fs.ErrNotExist):
			return cfg, rerr
		}
	}
	if env.Getenv != nil {
		if v := env.Getenv("APOTHECARY_URL"); v != "" {
			cfg.URL = v
		}
		if v := env.Getenv("APOTHECARY_TOKEN"); v != "" {
			cfg.Token, cfg.TokenID = v, ""
		}
	}
	return cfg, nil
}

func saveConfig(env Env, cfg Config) error {
	path, err := configPath(env)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func removeConfig(env Env) error {
	path, err := configPath(env)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
