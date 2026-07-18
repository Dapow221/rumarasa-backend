package config

import (
	"fmt"
	"os"
	"strings"
)

type Config struct {
	Env           string // "dev" or "prod"
	Port          string
	DatabaseURL   string
	JWTSecret     []byte
	CORSOrigins   []string
	AdminUsername string
	AdminPassword string
}

func (c *Config) IsProd() bool { return c.Env == "prod" }

// Load reads configuration from the environment and fails fast on anything
// invalid so the process never starts half-configured.
func Load() (*Config, error) {
	c := &Config{
		Env:           getenv("APP_ENV", "dev"),
		Port:          getenv("PORT", "8080"),
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		JWTSecret:     []byte(os.Getenv("JWT_SECRET")),
		AdminUsername: os.Getenv("ADMIN_USERNAME"),
		AdminPassword: os.Getenv("ADMIN_PASSWORD"),
	}

	for _, origin := range strings.Split(getenv("CORS_ORIGINS", "http://localhost:3000"), ",") {
		if o := strings.TrimSpace(origin); o != "" {
			c.CORSOrigins = append(c.CORSOrigins, o)
		}
	}

	var errs []string
	if c.DatabaseURL == "" {
		errs = append(errs, "DATABASE_URL is required")
	}
	if len(c.JWTSecret) < 32 {
		errs = append(errs, "JWT_SECRET is required and must be at least 32 characters")
	}
	if c.Env != "dev" && c.Env != "prod" {
		errs = append(errs, `APP_ENV must be "dev" or "prod"`)
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("invalid configuration:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return c, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
