package config

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	DBHost          string
	DBPort          string
	DBUser          string
	DBPassword      string
	DBName          string
	ServerPort      string
	KiteAPIKey      string
	KiteAPISecret   string
	KiteAccessToken string
	KiteBaseURL     string
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	cfg := &Config{
		DBHost:          getEnv("DB_HOST", "localhost"),
		DBPort:          getEnv("DB_PORT", "5432"),
		DBUser:          getEnv("DB_USER", "postgres"),
		DBPassword:      getEnv("DB_PASSWORD", ""),
		DBName:          getEnv("DB_NAME", "stocks"),
		ServerPort:      getEnv("SERVER_PORT", "8080"),
		KiteAPIKey:      getEnv("KITE_API_KEY", ""),
		KiteAPISecret:   getEnv("KITE_API_SECRET", ""),
		KiteAccessToken: getEnv("KITE_ACCESS_TOKEN", ""),
		KiteBaseURL:     getEnv("KITE_BASE_URL", "https://api.kite.trade"),
	}
	return cfg, nil
}

func (c *Config) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		c.DBHost, c.DBPort, c.DBUser, c.DBPassword, c.DBName,
	)
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
