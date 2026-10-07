package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// envFilePath is where the bot looks for settings when it runs straight on a
// host, under systemd for instance. In a container there is no such file —
// docker compose passes .env as the environment. It is a single fixed location,
// not a search: the bot either finds settings there or gets them from the
// environment, with nothing to work out at startup.
const envFilePath = "/etc/aksha/.env"

// ApplyEnvFile loads /etc/aksha/.env, if there is one, into the environment.
func ApplyEnvFile() error {
	if err := loadEnvFile(envFilePath); err != nil {
		return fmt.Errorf("reading %s: %w", envFilePath, err)
	}
	return nil
}

// loadEnvFile applies the file to the process environment. A variable that is
// already set wins, so the real environment always overrides the file.
//
// The syntax is docker's --env-file, so the same file behaves identically
// whether it is baked in, passed with --env-file, or read by `make run`: one
// KEY=VALUE per line, the value taken literally, and only a whole-line # is a
// comment.
func loadEnvFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing baked in; the environment is the only source
		}
		return err
	}
	defer f.Close()

	scan := bufio.NewScanner(f)
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			continue
		}
		if _, set := os.LookupEnv(key); !set {
			if err := os.Setenv(key, value); err != nil {
				return err
			}
		}
	}
	return scan.Err()
}
