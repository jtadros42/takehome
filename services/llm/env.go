package llm

import "github.com/kelseyhightower/envconfig"

type EnvConfig struct {
	ProjectID    string `envconfig:"GOOGLE_CLOUD_PROJECT" default:"evertune-tests"`
	Region       string `envconfig:"VERTEX_LOCATION" default:"us-central1"`
	HttpPort     int    `envconfig:"HTTP_PORT" default:"8081"`
	TemporalHost string `envconfig:"TEMPORAL_HOST" default:"localhost"`
	TemporalPort int    `envconfig:"TEMPORAL_PORT" default:"7233"`
}

func NewConfigFromEnv() (EnvConfig, error) {
	var e EnvConfig
	err := envconfig.Process("", &e)
	return e, err
}
