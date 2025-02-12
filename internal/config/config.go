package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Name        string     `yaml:"name"`
	Directories []string   `yaml:"directories"`
	Databases   []Database `yaml:"databases"`
	Schedule    string     `yaml:"schedule"`
}

type Database struct {
	Name     string `yaml:"name"`
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	DBName   string `yaml:"dbname"`
	User     string `yaml:"user"`
	Schema   string `yaml:"schema"`
	Password string `yaml:"password"`
	SSLMode  string `yaml:"sslmode"`
}

func LoadConfig(filename string) (*Config, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, err
	}

	return &config, nil
}
