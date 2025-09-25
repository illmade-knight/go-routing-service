package cmd

import (
	_ "embed" // Required for go:embed
)

//go:embed prod/config.yaml
var ConfigYAML []byte

// REFACTOR: Added structs to support selectable cache backends.
type YamlRedisConfig struct {
	Addr string `yaml:"addr"`
}

type YamlFirestoreConfig struct {
	CollectionName string `yaml:"collection_name"`
}

type YamlPresenceCacheConfig struct {
	Type      string              `yaml:"type"`
	Redis     YamlRedisConfig     `yaml:"redis"`
	Firestore YamlFirestoreConfig `yaml:"firestore"`
}

type YamlCorsConfig struct {
	AllowedOrigins []string `yaml:"allowed_origins"`
	Role           string   `yaml:"role"`
}

// YamlConfig defines the structure for unmarshaling the embedded config.yaml file.
type YamlConfig struct {
	ProjectID             string                  `yaml:"project_id"`
	RunMode               string                  `yaml:"run_mode"`
	APIPort               string                  `yaml:"api_port"`
	WebSocketPort         string                  `yaml:"websocket_port"`
	Cors                  YamlCorsConfig          `yaml:"cors"`
	PresenceCache         YamlPresenceCacheConfig `yaml:"presence_cache"`
	IngressTopicID        string                  `yaml:"ingress_topic_id"`
	IngressSubscriptionID string                  `yaml:"ingress_subscription_id"`
	DeliveryTopicID       string                  `yaml:"delivery_topic_id"`
}

// AppConfig holds the final, validated configuration for the application.
type AppConfig struct {
	ProjectID             string
	RunMode               string
	APIPort               string
	WebSocketPort         string
	Cors                  YamlCorsConfig
	PresenceCache         YamlPresenceCacheConfig // Keep the structured config
	IngressTopicID        string
	IngressSubscriptionID string
	DeliveryTopicID       string
	JWTSecret             string
}
