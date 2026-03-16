package main

import (
	"os"
	"strings"
)

type config struct {
	Server string
	Side   string
}

// loadConfig reads config.toml from the same directory as the binary (or cwd).
// Only bare key = "value" lines are supported. Unrecognised keys are ignored.
// Missing file is not an error — returns an empty config.
func loadConfig() config {
	paths := []string{
		"config.toml",
		configDirPath(),
	}
	for _, p := range paths {
		if p == "" {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		return parseToml(data)
	}
	return config{}
}

// configDirPath returns ~/.config/kvmux/config.toml if $HOME is set.
func configDirPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home + "/.config/kvmux/config.toml"
}

func parseToml(data []byte) config {
	var c config
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		v = strings.Trim(v, `"'`)
		switch k {
		case "server":
			c.Server = v
		case "side":
			c.Side = v
		}
	}
	return c
}
