/*
 * Copyright (c) 2026 InfAI (CC SES)
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *    http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package config

import (
	"flag"
	"time"

	sb_config_hdl "github.com/SENERGY-Platform/go-service-base/config-hdl"
	sb_config_env_parser "github.com/SENERGY-Platform/go-service-base/config-hdl/env_parser"
	sb_config_types "github.com/SENERGY-Platform/go-service-base/config-hdl/types"
)

type Config struct {
	LogLevel    string `json:"log_level" env_var:"LOG_LEVEL"`
	ConnectorId string `json:"connector_id" env_var:"CONNECTOR_ID"`

	MgwMqttBroker string                 `json:"mgw_mqtt_broker" env_var:"MGW_MQTT_BROKER"`
	MgwMqttUser   string                 `json:"mgw_mqtt_user" env_var:"MGW_MQTT_USER"`
	MgwMqttPw     sb_config_types.Secret `json:"mgw_mqtt_pw" env_var:"MGW_MQTT_PW"`

	// PlatformApiUrl points to the api gateway of the mgw cloud proxy, which adds the client certificate of the gateway.
	PlatformApiUrl            string `json:"platform_api_url" env_var:"PLATFORM_API_URL"`
	PlatformProtocolId        string `json:"platform_protocol_id" env_var:"PLATFORM_PROTOCOL_ID"`
	PlatformProtocolSegmentId string `json:"platform_protocol_segment_id" env_var:"PLATFORM_PROTOCOL_SEGMENT_ID"`

	ConnectLifeUsername          string                 `json:"connectlife_username" env_var:"CONNECTLIFE_USERNAME"`
	ConnectLifePassword          sb_config_types.Secret `json:"connectlife_password" env_var:"CONNECTLIFE_PASSWORD"`
	ConnectLifeGigyaApiKey       sb_config_types.Secret `json:"connectlife_gigya_api_key" env_var:"CONNECTLIFE_GIGYA_API_KEY"`
	ConnectLifeOAuthClientSecret sb_config_types.Secret `json:"connectlife_oauth_client_secret" env_var:"CONNECTLIFE_OAUTH_CLIENT_SECRET"`
	ConnectLifeAppSecret         sb_config_types.Secret `json:"connectlife_app_secret" env_var:"CONNECTLIFE_APP_SECRET"`
	ConnectLifeSignSalt          sb_config_types.Secret `json:"connectlife_sign_salt" env_var:"CONNECTLIFE_SIGN_SALT"`

	PollInterval   time.Duration `json:"poll_interval" env_var:"POLL_INTERVAL"`
	DeviceIdPrefix string        `json:"device_id_prefix" env_var:"DEVICE_ID_PREFIX"`
}

var ConfPath string

func ParseFlags() {
	flag.StringVar(&ConfPath, "config", "", "path to config JSON file")
	flag.Parse()
}

func New(path string) (*Config, error) {
	cfg := Config{
		LogLevel:                  "info",
		ConnectorId:               "mgw-connectlife-dc",
		MgwMqttBroker:             "tcp://localhost:1883",
		PlatformApiUrl:            "http://gateway",
		PlatformProtocolId:        "urn:infai:ses:protocol:f3a63aeb-187e-4dd9-9ef5-d97a6eb6292b",
		PlatformProtocolSegmentId: "urn:infai:ses:protocol-segment:0d211842-cef8-41ec-ab6b-9dbc31bc3a65",
		PollInterval:              time.Minute,
		DeviceIdPrefix:            "connectlife-",
		// values of the ConnectLife app, identical for every account
		ConnectLifeGigyaApiKey:       "4_yhTWQmHFpZkQZDSV1uV-_A",
		ConnectLifeOAuthClientSecret: "07swfKgvJhC3ydOUS9YV_SwVz0i4LKqlOLGNUukYHVMsJRF1b-iWeUGcNlXyYCeK",
		ConnectLifeAppSecret:         "yOzhz6junYno-nmULM3Wr7PU_dpSZN22ZdluvVWZ4uW5ZwwG8fIGCHTbrhcnU-iv",
		ConnectLifeSignSalt:          "D9519A4B756946F081B7BB5B5E8D1197",
	}
	err := sb_config_hdl.Load(&cfg, nil, []sb_config_hdl.EnvTypeParser{
		sb_config_types.SecretEnvTypeParser,
		sb_config_env_parser.DurationEnvTypeParser,
	}, nil, path)
	return &cfg, err
}
