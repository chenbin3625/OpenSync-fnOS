package config

import (
	"os"
	"strings"
)

func DataDir() string {
	if value := strings.TrimSpace(os.Getenv("TRIM_PKGVAR")); value != "" {
		return value
	}
	if value := strings.TrimSpace(os.Getenv("OPENSYNC_DATA_DIR")); value != "" {
		return value
	}
	return "data"
}

func ConfigDir() string {
	if value := strings.TrimSpace(os.Getenv("TRIM_PKGETC")); value != "" {
		return value
	}
	return DataDir()
}
