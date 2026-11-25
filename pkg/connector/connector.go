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

// Package connector polls the ConnectLife cloud and maps appliances to mgw devices.
package connector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SENERGY-Platform/mgw-connectlife-dc/pkg/connectlife"
	"github.com/SENERGY-Platform/mgw-connectlife-dc/pkg/devicetype"
	"github.com/SENERGY-Platform/mgw-dc-lib-go/pkg/mgw"
	"github.com/SENERGY-Platform/models/go/models"
)

type Cloud interface {
	GetAppliances(ctx context.Context) ([]connectlife.Appliance, error)
	SetProperties(ctx context.Context, puid string, properties map[string]string) error
	GetProperties(ctx context.Context, deviceTypeCode, deviceFeatureCode string) ([]connectlife.PropertyInfo, error)
}

type DeviceTypes interface {
	Ensure(ctx context.Context, attrKey, attrValue string, candidate func() (models.DeviceType, error)) (models.DeviceType, bool, error)
}

type Mgw interface {
	SetDevice(device Device) error
	MarshalAndSendEvent(deviceId string, serviceId string, value interface{}) error
}

// Device is the mgw view of one appliance.
type Device struct {
	Id           string
	Name         string
	DeviceTypeId string
	Puid         string
	Online       bool
	Types        map[string]models.Type
	Status       map[string]any
	// Writable lists the properties the model accepts writes for; nil if unknown
	Writable map[string]bool
}

func (d Device) GetInfo() mgw.DeviceInfo {
	state := mgw.Offline
	if d.Online {
		state = mgw.Online
	}
	return mgw.DeviceInfo{Id: d.Id, Name: d.Name, State: state, DeviceType: d.DeviceTypeId}
}

type Connector struct {
	cloud       Cloud
	deviceTypes DeviceTypes
	mgw         Mgw
	protocol    devicetype.Protocol
	idPrefix    string
	logger      *slog.Logger

	mux     sync.Mutex
	devices map[string]Device
	// announced holds the last DeviceInfo sent to the device-manager, to send updates only on change
	announced map[string]mgw.DeviceInfo
	// warned avoids repeating the warning about properties the device type lacks on every poll
	warned map[string]bool
	// propertyInfo caches the property list per appliance model; it does not change at runtime
	propertyInfo map[string][]connectlife.PropertyInfo
}

func New(cloud Cloud, deviceTypes DeviceTypes, protocol devicetype.Protocol, idPrefix string, logger *slog.Logger) *Connector {
	return &Connector{
		cloud:        cloud,
		deviceTypes:  deviceTypes,
		protocol:     protocol,
		idPrefix:     idPrefix,
		logger:       logger,
		devices:      map[string]Device{},
		announced:    map[string]mgw.DeviceInfo{},
		warned:       map[string]bool{},
		propertyInfo: map[string][]connectlife.PropertyInfo{},
	}
}

// SetMgw is separate from New because the mgw client needs the refresh callback of the connector.
func (c *Connector) SetMgw(m Mgw) {
	c.mux.Lock()
	defer c.mux.Unlock()
	c.mgw = m
}

// MinPollInterval protects the ConnectLife account; Gigya locks accounts after repeated logins.
const MinPollInterval = 10 * time.Second

const maxBackoff = time.Hour

// Run polls until ctx ends. After failed polls the wait doubles up to one hour.
func (c *Connector) Run(ctx context.Context, interval time.Duration) {
	interval = max(interval, MinPollInterval)
	wait := interval
	for {
		if err := c.Poll(ctx); err != nil && ctx.Err() == nil {
			wait = min(wait*2, max(maxBackoff, interval))
			c.logger.Warn("poll failed", "error", err, "next_poll_in", wait.String())
		} else {
			wait = interval
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// Poll reads all appliances, registers new or changed devices and sends a status event per online device.
func (c *Connector) Poll(ctx context.Context) error {
	appliances, err := c.cloud.GetAppliances(ctx)
	if err != nil {
		return err
	}
	c.mux.Lock()
	defer c.mux.Unlock()
	if c.mgw == nil {
		return errors.New("mgw client not ready")
	}
	seen := map[string]bool{}
	var errs []error
	for _, a := range appliances {
		device, err := c.device(ctx, a)
		if err != nil {
			errs = append(errs, fmt.Errorf("appliance %s (%s): %w", a.DeviceId, a.TypeKey(), err))
			continue
		}
		seen[device.Id] = true
		c.devices[device.Id] = device
		if err = c.announce(device); err != nil {
			errs = append(errs, err)
			continue
		}
		if device.Online {
			if err = c.mgw.MarshalAndSendEvent(device.Id, devicetype.StatusService, devicetype.StatusEvent(device.Status, device.Types)); err != nil {
				errs = append(errs, fmt.Errorf("send status of %s: %w", device.Id, err))
			}
		}
	}
	// an appliance removed from the account is reported offline, not deleted, so its history stays
	for id, device := range c.devices {
		if !seen[id] && device.Online {
			device.Online = false
			c.devices[id] = device
			if err = c.announce(device); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (c *Connector) device(ctx context.Context, a connectlife.Appliance) (Device, error) {
	if a.StatusList == nil {
		// the gateway omits statusList now and then; the last known values stay valid
		if known, ok := c.devices[c.idPrefix+a.DeviceId]; ok {
			a.StatusList = known.Status
		}
	}
	info, ok := c.propertyInfo[a.TypeKey()]
	if !ok {
		var err error
		info, err = c.cloud.GetProperties(ctx, a.DeviceTypeCode, a.DeviceFeatureCode)
		if err != nil {
			// the status list still allows a device type; the list is asked again on the next poll
			c.logger.Warn("unable to read property list, using status list only", "connectlife_type", a.TypeKey(), "error", err)
		} else {
			c.propertyInfo[a.TypeKey()] = info
		}
	}
	dt, created, err := c.deviceTypes.Ensure(ctx, devicetype.AttrType, a.TypeKey(), func() (models.DeviceType, error) {
		return devicetype.Generate(a, info, c.protocol)
	})
	if err != nil {
		return Device{}, err
	}
	if created {
		c.logger.Info("created device type", "device_type_id", dt.Id, "connectlife_type", a.TypeKey())
	}
	types := devicetype.PropertyTypes(dt)
	if missing := devicetype.MissingProperties(a.StatusList, types); len(missing) > 0 && !c.warned[a.DeviceId] {
		c.warned[a.DeviceId] = true
		c.logger.Warn("device type lacks properties of appliance, values are not forwarded", "device_type_id", dt.Id, "properties", strings.Join(missing, ","))
	}
	name := strings.TrimSpace(a.DeviceNickName)
	if name == "" {
		name = strings.TrimSpace(a.DeviceTypeName + " " + a.DeviceId)
	}
	return Device{
		Id:           c.idPrefix + a.DeviceId,
		Name:         name,
		DeviceTypeId: dt.Id,
		Puid:         a.Puid,
		Online:       a.Online(),
		Types:        types,
		Status:       a.StatusList,
		Writable:     devicetype.Writable(info),
	}, nil
}

func (c *Connector) announce(device Device) error {
	info := device.GetInfo()
	if c.announced[device.Id] == info {
		return nil
	}
	if err := c.mgw.SetDevice(device); err != nil {
		return fmt.Errorf("set device %s: %w", device.Id, err)
	}
	c.announced[device.Id] = info
	return nil
}

// Refresh announces all known devices again, as the device-manager asks for after its restart.
func (c *Connector) Refresh() {
	c.mux.Lock()
	defer c.mux.Unlock()
	c.announced = map[string]mgw.DeviceInfo{}
	if c.mgw == nil {
		return
	}
	for _, device := range c.devices {
		if err := c.announce(device); err != nil {
			c.logger.Warn("refresh failed", "error", err)
		}
	}
}

// Status answers a request on the status service with the values of the last poll.
func (c *Connector) Status(device Device, _ interface{}) (interface{}, error) {
	c.mux.Lock()
	defer c.mux.Unlock()
	current, ok := c.devices[device.Id]
	if !ok {
		return nil, errors.New("unknown device")
	}
	return devicetype.StatusEvent(current.Status, current.Types), nil
}

// SetProperty writes one property. Input is {"property": "...", "value": ...}.
func (c *Connector) SetProperty(device Device, input interface{}) (interface{}, error) {
	command, ok := input.(map[string]interface{})
	if !ok {
		return nil, errors.New("expected object with property and value")
	}
	property, _ := command["property"].(string)
	if property == "" {
		return nil, errors.New("missing property")
	}
	c.mux.Lock()
	writable := c.devices[device.Id].Writable
	c.mux.Unlock()
	if writable != nil && !writable[property] {
		return nil, fmt.Errorf("property %s is not writable", property)
	}
	value, err := valueString(command["value"])
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err = c.cloud.SetProperties(ctx, device.Puid, map[string]string{property: value}); err != nil {
		return nil, err
	}
	c.mux.Lock()
	if current, ok := c.devices[device.Id]; ok && current.Status != nil {
		current.Status[property] = value
	}
	c.mux.Unlock()
	return nil, nil
}

func valueString(v interface{}) (string, error) {
	switch value := v.(type) {
	case string:
		return value, nil
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64), nil
	case bool:
		if value {
			return "1", nil
		}
		return "0", nil
	case nil:
		return "", errors.New("missing value")
	default:
		return "", fmt.Errorf("unsupported value type %T", v)
	}
}
