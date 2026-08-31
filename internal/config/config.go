// Package config loads application configuration from a .env file.
package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds everything the server needs to boot.
type Config struct {
	CHHost     string
	CHPort     int
	CHDatabase string
	CHUsername string
	CHPassword string
	// CHProtocol is "native" (default, port 9000) or "http" (port 8123).
	CHProtocol string

	// HTTPAddr is the address the web server listens on, e.g. ":8080".
	HTTPAddr string
}

// Load reads envPath into a Config. Values from the .env file take
// precedence over same-named OS environment variables — this matters on
// Windows in particular, which predefines generic-looking variables like
// USERNAME (the logged-in OS account) that would otherwise silently shadow
// a .env file using the same key for something unrelated, like a database
// user. Real OS env vars are only used as a fallback for keys the .env
// file doesn't set, so ops-style overrides (e.g. HTTP_ADDR in a container)
// still work.
func Load(envPath string) (*Config, error) {
	fileVars, err := parseDotEnv(envPath)
	if err != nil {
		return nil, err
	}

	get := func(key, fallback string) string {
		if v, ok := fileVars[key]; ok {
			return v
		}
		if v, ok := os.LookupEnv(key); ok {
			return v
		}
		return fallback
	}

	cfg := &Config{
		CHHost:     get("HOST", "127.0.0.1"),
		CHDatabase: get("DATABASE", "default"),
		CHUsername: get("USERNAME", "default"),
		CHPassword: get("PASSWORD", ""),
		CHProtocol: strings.ToLower(get("CH_PROTOCOL", "native")),
		HTTPAddr:   get("HTTP_ADDR", ":8080"),
	}
	if cfg.CHProtocol != "native" && cfg.CHProtocol != "http" {
		return nil, fmt.Errorf("config: CH_PROTOCOL must be \"native\" or \"http\", got %q", cfg.CHProtocol)
	}

	portStr := get("PORT", "9000")
	port, err := strconv.Atoi(strings.TrimSpace(portStr))
	if err != nil {
		return nil, fmt.Errorf("config: invalid PORT %q: %w", portStr, err)
	}
	cfg.CHPort = port

	if cfg.CHHost == "" {
		return nil, fmt.Errorf("config: HOST is required")
	}
	if cfg.CHDatabase == "" {
		return nil, fmt.Errorf("config: DATABASE is required")
	}

	return cfg, nil
}

// parseDotEnv parses a simple KEY=VALUE .env file, ignoring blank lines and
// lines starting with '#'. A missing file is not an error — it just yields
// an empty map, so pure-OS-env configuration still works.
func parseDotEnv(path string) (map[string]string, error) {
	vars := make(map[string]string)

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return vars, nil
		}
		return nil, fmt.Errorf("config: reading %s: %w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, "=")
		if idx < 0 {
			return nil, fmt.Errorf("config: %s:%d: expected KEY=VALUE, got %q", path, lineNo, line)
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		val = strings.Trim(val, `"'`)
		if key == "" {
			continue
		}
		vars[key] = val
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("config: reading %s: %w", path, err)
	}
	return vars, nil
}
