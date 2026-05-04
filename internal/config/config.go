package config

import "os"

type Config struct {
	APIListen string
	DBURL     string
}

func Load() *Config {
	return &Config{
		APIListen: getenv("TXMILL_API_LISTEN", ":8080"),
		DBURL:     os.Getenv("TXMILL_DB_URL"),
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
