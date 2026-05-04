package config

import "os"

type Config struct {
	APIListen string
}

func Load() *Config {
	return &Config{
		APIListen: getenv("TXMILL_API_LISTEN", ":8080"),
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
