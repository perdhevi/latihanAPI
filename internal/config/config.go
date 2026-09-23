package config

import (
	"fmt"
	"log/slog"
	"net"
	"os"
)

type Config struct {
	HTTPAddr    string
	DatabaseURL string
	LogLevel    slog.Level
}

func Load() (Config, error) {
	c := Config{HTTPAddr: os.Getenv("HTTP_ADDR"), DatabaseURL: os.Getenv("DATABASE_URL")}
	if c.HTTPAddr == "" {
		c.HTTPAddr = ":8080"
	}
	if _, _, err := net.SplitHostPort(c.HTTPAddr); err != nil {
		return Config{}, fmt.Errorf("HTTP_ADDR must be host:port: %w", err)
	}
	if c.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if level := os.Getenv("LOG_LEVEL"); level != "" {
		if err := c.LogLevel.UnmarshalText([]byte(level)); err != nil {
			return Config{}, fmt.Errorf("invalid LOG_LEVEL")
		}
	}
	return c, nil
}
