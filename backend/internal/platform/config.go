// Package platform holds process-level concerns: configuration, logging and database access.
package platform

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config is loaded exclusively from environment variables.
type Config struct {
	DatabaseURL     string
	HTTPAddr        string
	JWTSecret       []byte
	AccessTTL       time.Duration
	RefreshTTL      time.Duration
	RequestTimeout  time.Duration
	ShutdownTimeout time.Duration
	CORSOrigin      string
	Location        *time.Location
	LogLevel        string
}

func LoadConfig() (Config, error) {
	loc, err := time.LoadLocation(env("CLINIC_TZ", "Europe/Istanbul"))
	if err != nil {
		return Config{}, fmt.Errorf("CLINIC_TZ: %w", err)
	}
	c := Config{
		DatabaseURL:     os.Getenv("DATABASE_URL"),
		HTTPAddr:        env("HTTP_ADDR", ":8080"),
		JWTSecret:       []byte(os.Getenv("JWT_SECRET")),
		AccessTTL:       envDuration("JWT_ACCESS_TTL", 15*time.Minute),
		RefreshTTL:      envDuration("JWT_REFRESH_TTL", 7*24*time.Hour),
		RequestTimeout:  envDuration("REQUEST_TIMEOUT", 15*time.Second),
		ShutdownTimeout: envDuration("SHUTDOWN_TIMEOUT", 20*time.Second),
		CORSOrigin:      env("CORS_ORIGIN", "http://localhost:3000"),
		Location:        loc,
		LogLevel:        env("LOG_LEVEL", "info"),
	}
	if c.DatabaseURL == "" {
		return c, errors.New("DATABASE_URL is required")
	}
	if len(c.JWTSecret) < 32 {
		return c, errors.New("JWT_SECRET must be at least 32 bytes")
	}
	return c, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second
	}
	return def
}
