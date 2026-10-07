package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The file is read exactly as `docker run --env-file` reads it: values are taken
// literally, so a password containing # survives and quotes are never stripped.
func TestLoadEnvFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	body := "# a comment\n" +
		"SIMPLE=hello\n" +
		"\n" +
		"WITH_HASH=postgres://u:p#ss@localhost:5432/db\n" +
		"WITH_SPACE=two words\n" +
		"EMPTY=\n" +
		"QUOTED=\"kept as written\"\n" +
		"ALREADY_SET=from file\n" +
		"  INDENTED=trimmed key\n" +
		"nonsense line without an equals sign\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ALREADY_SET", "from environment")
	if err := loadEnvFile(path); err != nil {
		t.Fatalf("loadEnvFile: %v", err)
	}

	for key, want := range map[string]string{
		"SIMPLE":      "hello",
		"WITH_HASH":   "postgres://u:p#ss@localhost:5432/db",
		"WITH_SPACE":  "two words",
		"EMPTY":       "",
		"QUOTED":      `"kept as written"`,
		"INDENTED":    "trimmed key",
		"ALREADY_SET": "from environment", // the environment wins
	} {
		t.Setenv(key, os.Getenv(key)) // restored when the test ends
		if got := os.Getenv(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

// No file baked in is the normal case outside the image, not an error.
func TestLoadEnvFileMissing(t *testing.T) {
	if err := loadEnvFile(filepath.Join(t.TempDir(), "absent")); err != nil {
		t.Fatalf("a missing file should be ignored, got %v", err)
	}
}
