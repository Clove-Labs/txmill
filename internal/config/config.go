package config

import (
	"os"
	"strconv"
)

type Config struct {
	APIListen         string
	DBURL             string
	KeystoreDir       string
	KeystorePassword  string
	RPCURL            string
	ChainID           uint64
	WatcherIntervalMs uint64
	WatcherBatchSize  uint64
	GasIntervalMs     uint64
}

func Load() *Config {
	return &Config{
		APIListen:         getenv("TXMILL_API_LISTEN", ":8080"),
		DBURL:             os.Getenv("TXMILL_DB_URL"),
		KeystoreDir:       getenv("TXMILL_KEYSTORE_DIR", "./data/keys"),
		KeystorePassword:  os.Getenv("TXMILL_KEYSTORE_PASSWORD"),
		RPCURL:            os.Getenv("TXMILL_RPC_URL"),
		ChainID:           getenvUint64("TXMILL_CHAIN_ID", 146),
		WatcherIntervalMs: getenvUint64("TXMILL_WATCHER_INTERVAL_MS", 1000),
		WatcherBatchSize:  getenvUint64("TXMILL_WATCHER_BATCH_SIZE", 50),
		GasIntervalMs:     getenvUint64("TXMILL_GAS_INTERVAL_MS", 30_000),
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvUint64(key string, def uint64) uint64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return def
	}
	return n
}
