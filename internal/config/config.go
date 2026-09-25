package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
)

type Config struct {
	HTTPAddr    string
	DatabaseURL string
	LogLevel    slog.Level
	// AuthProvider names the registered authentication provider. There is no
	// default: a missing setting must never leave the API open.
	AuthProvider string
}

func Load() (Config, error) {
	c := Config{HTTPAddr: os.Getenv("HTTP_ADDR"), DatabaseURL: os.Getenv("DATABASE_URL"), AuthProvider: os.Getenv("AUTH_PROVIDER")}
	if c.HTTPAddr == "" {
		c.HTTPAddr = ":8080"
	}
	if _, _, err := net.SplitHostPort(c.HTTPAddr); err != nil {
		return Config{}, fmt.Errorf("HTTP_ADDR must be host:port: %w", err)
	}
	if c.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	if c.AuthProvider == "" {
		return Config{}, errors.New("AUTH_PROVIDER is required (for example firebase, cognito or oidc)")
	}
	if level := os.Getenv("LOG_LEVEL"); level != "" {
		if err := c.LogLevel.UnmarshalText([]byte(level)); err != nil {
			return Config{}, errors.New("invalid LOG_LEVEL")
		}
	}
	return c, nil
}
