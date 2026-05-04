package config

import "os"

type Config struct {
	APIListen        string
	DBURL            string
	KeystoreDir      string
	KeystorePassword string
}

func Load() *Config {
	return &Config{
		APIListen:        getenv("TXMILL_API_LISTEN", ":8080"),
		DBURL:            os.Getenv("TXMILL_DB_URL"),
		KeystoreDir:      getenv("TXMILL_KEYSTORE_DIR", "./data/keys"),
		KeystorePassword: os.Getenv("TXMILL_KEYSTORE_PASSWORD"),
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
