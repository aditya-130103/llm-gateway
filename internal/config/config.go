package config

import (
	"os"
	"strconv"

	yaml "gopkg.in/yaml.v3"
)

type Team struct {
	Name string `yaml:"name"`
	KeyHash string `yaml:"key_hash"`
	Email string `yaml:"email"`
	RpmLimit int `yaml:"rpm_limit"`
}

type Upstream struct {
	Name string `yaml:"name"`
	BaseUrl string `yaml:"base_url"`
	ApiKey string `yaml:"api_key"`
	Model string `yaml:"model"`
}

type Embedding struct {
	ApiKey 	            string `yaml:"api_key"`
	BaseUrl             string `yaml:"base_url"`
	Model               string  `yaml:"model"`
	SimilarityThreshold float64 `yaml:"similarity_threshold"`
	TTLSeconds          int     `yaml:"ttl_seconds"`
}

type Config struct {
	Upstreams []Upstream `yaml:"upstream"`
	Teams     []Team     `yaml:"teams"`
	Redis     Redis      `yaml:"redis"`
	Embedding Embedding  `yaml:"embedding"`
}

type Redis struct {
	Host string `yaml:"host"`
	Port int `yaml:"port"`
	Password string `yaml:"password"`
}

func LoadConfig(path string) (Config, error) {
	yamlFile, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var config Config
	if err := yaml.Unmarshal(yamlFile, &config); err != nil {
		return Config{}, err
	}
	if host := os.Getenv("REDIS_HOST"); host != "" {
		config.Redis.Host = host
	}
	if portStr := os.Getenv("REDIS_PORT"); portStr != "" {
		if port, err := strconv.Atoi(portStr); err == nil {
			config.Redis.Port = port
		}
	}
	if pw := os.Getenv("REDIS_PASSWORD"); pw != "" {
		config.Redis.Password = pw
	}
	return config, nil
}