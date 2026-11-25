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

package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"syscall"

	"github.com/SENERGY-Platform/mgw-connectlife-dc/pkg/config"
	"github.com/SENERGY-Platform/mgw-connectlife-dc/pkg/connectlife"
	"github.com/SENERGY-Platform/mgw-connectlife-dc/pkg/connector"
	"github.com/SENERGY-Platform/mgw-connectlife-dc/pkg/devicetype"
	"github.com/SENERGY-Platform/mgw-connectlife-dc/pkg/platform"
	"github.com/SENERGY-Platform/mgw-connectlife-dc/pkg/util"
	"github.com/SENERGY-Platform/mgw-dc-lib-go/pkg/configuration"
	"github.com/SENERGY-Platform/mgw-dc-lib-go/pkg/mgw"
)

func main() {
	ec := 0
	defer func() {
		os.Exit(ec)
	}()

	config.ParseFlags()
	cfg, err := config.New(config.ConfPath)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		ec = 1
		return
	}
	util.InitStructLogger(cfg.LogLevel)
	logger := util.Logger

	cloud, err := connectlife.New(connectlife.Credentials{
		Username:          cfg.ConnectLifeUsername,
		Password:          cfg.ConnectLifePassword.Value(),
		GigyaApiKey:       cfg.ConnectLifeGigyaApiKey.Value(),
		OAuthClientSecret: cfg.ConnectLifeOAuthClientSecret.Value(),
		AppSecret:         cfg.ConnectLifeAppSecret.Value(),
		SignSalt:          cfg.ConnectLifeSignSalt.Value(),
	})
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		ec = 1
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		util.Wait(ctx, logger, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
		cancel()
	}()

	conn := connector.New(
		cloud,
		platform.New(cfg.PlatformApiUrl),
		devicetype.Protocol{Id: cfg.PlatformProtocolId, SegmentId: cfg.PlatformProtocolSegmentId},
		cfg.DeviceIdPrefix,
		logger,
	)

	wg := &sync.WaitGroup{}
	mgwClient, err := mgw.New[connector.Device](configuration.Config{
		ConnectorId:   cfg.ConnectorId,
		MgwMqttBroker: cfg.MgwMqttBroker,
		MgwMqttUser:   cfg.MgwMqttUser,
		MgwMqttPw:     cfg.MgwMqttPw.Value(),
		Debug:         cfg.LogLevel == "debug",
	}, ctx, wg, conn.Refresh)
	if err != nil {
		logger.Error("unable to connect to mgw broker", "error", err)
		ec = 1
		return
	}
	mgwClient.RegisterService(devicetype.StatusService, conn.Status, nil)
	mgwClient.RegisterService(devicetype.SetPropertyService, conn.SetProperty, nil)
	for _, id := range devicetype.ActionServiceIds() {
		mgwClient.RegisterService(id, conn.SetProperty, nil)
	}
	conn.SetMgw(mgwClient)

	conn.Run(ctx, cfg.PollInterval)
	wg.Wait()
}
