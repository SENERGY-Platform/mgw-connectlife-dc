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

package connectlife

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Appliance is one entry of the deviceList returned by the ConnectLife gateway.
type Appliance struct {
	WifiId            string         `json:"wifiId"`
	DeviceId          string         `json:"deviceId"`
	Puid              string         `json:"puid"`
	DeviceNickName    string         `json:"deviceNickName"`
	DeviceFeatureCode string         `json:"deviceFeatureCode"`
	DeviceFeatureName string         `json:"deviceFeatureName"`
	DeviceTypeCode    string         `json:"deviceTypeCode"`
	DeviceTypeName    string         `json:"deviceTypeName"`
	RoomName          string         `json:"roomName"`
	OfflineState      FlexInt        `json:"offlineState"`
	StatusList        map[string]any `json:"statusList"`
}

// Online reports the connection state. The gateway sends offlineState 1 for online and 0 for offline.
func (a Appliance) Online() bool {
	return a.OfflineState == 1
}

// TypeKey identifies the property set of an appliance model. Appliances with the same key share a device type.
func (a Appliance) TypeKey() string {
	return a.DeviceTypeCode + "-" + a.DeviceFeatureCode
}

// FlexInt accepts a JSON number or a numeric string.
type FlexInt int64

func (f *FlexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	*f = FlexInt(v)
	return nil
}

type gatewayEnvelope struct {
	Response json.RawMessage `json:"response"`
}

type gatewayResponse struct {
	ResultCode any    `json:"resultCode"`
	ErrorCode  any    `json:"errorCode"`
	ErrorDesc  string `json:"errorDesc"`
}

type deviceListResponse struct {
	DeviceList []Appliance `json:"deviceList"`
}

type tokenResponse struct {
	AccessToken  string  `json:"access_token"`
	ExpiresIn    FlexInt `json:"expires_in"`
	RefreshToken string  `json:"refresh_token"`
}

// PropertyInfo describes one property of an appliance model, as get_property_list returns it.
type PropertyInfo struct {
	Key       string  `json:"propertyKey"`
	Name      string  `json:"propertyName"`
	RW        string  `json:"propertyRW"`
	Type      FlexInt `json:"propertyType"`
	ValueList string  `json:"propertyValueList"`
	Lower     string  `json:"lowerLimit"`
	Upper     string  `json:"upperLimit"`
}

func (p PropertyInfo) Writable() bool {
	return strings.Contains(p.RW, "W")
}

type propertyListResponse struct {
	Properties []PropertyInfo `json:"properties"`
}
