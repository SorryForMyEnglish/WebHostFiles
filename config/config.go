package config

import (
	"gopkg.in/yaml.v2"
	"io/ioutil"
	"os"
)

type Config struct {
	TelegramToken   string  `yaml:"telegram_token"`
	CryptoBotToken  string  `yaml:"cryptobot_token"`
	XRocketToken    string  `yaml:"xrocket_token"`
	DatabasePath    string  `yaml:"database_path"`
	FileStoragePath string  `yaml:"file_storage_path"`
	MaxFileSize     int64   `yaml:"max_file_size"`
	Domain          string  `yaml:"domain"`
	HTTPAddress     string  `yaml:"http_address"`
	TLSCert         string  `yaml:"tls_cert"`
	TLSKey          string  `yaml:"tls_key"`
	PriceUpload     float64 `yaml:"price_upload"`
	PriceRefund     float64 `yaml:"price_refund"`
}

func Load(path string) (*Config, error) {
	cfg := &Config{}
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (c *Config) Save(path string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return ioutil.WriteFile(path, data, 0644)
}

func Ensure(path string) (*Config, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		cfg := &Config{
			TelegramToken:   "",
			CryptoBotToken:  "",
			XRocketToken:    "",
			DatabasePath:    "filestorage.db",
			FileStoragePath: "files",
			MaxFileSize:     100 * 1024 * 1024,
			Domain:          "http://localhost:8080",
			HTTPAddress:     ":8080",
			TLSCert:         "",
			TLSKey:          "",
			PriceUpload:     1.0,
			PriceRefund:     0.5,
		}
		if err := cfg.Save(path); err != nil {
			return nil, err
		}
		return cfg, nil
	}
	return Load(path)
}
