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

// Package platform finds and creates device types on the SENERGY platform.
//
// Requests go to the api gateway of the mgw cloud proxy, which authenticates them
// with the client certificate of the gateway. No token is sent from here.
package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/SENERGY-Platform/models/go/models"
)

type Client struct {
	baseUrl string
	http    *http.Client
	mux     sync.Mutex
	cache   map[string]models.DeviceType
}

func New(baseUrl string) *Client {
	return &Client{
		baseUrl: baseUrl,
		http:    &http.Client{Timeout: 30 * time.Second},
		cache:   map[string]models.DeviceType{},
	}
}

// Ensure returns the device type whose attribute key has the given value and creates it from
// candidate() if none exists. The device-manager rejects a second device type with the same
// attribute value; its check is not atomic, so two gateways creating at the same moment may
// both succeed. find then picks the smallest id, so all gateways settle on the same one.
func (c *Client) Ensure(ctx context.Context, attrKey, attrValue string, candidate func() (models.DeviceType, error)) (dt models.DeviceType, created bool, err error) {
	c.mux.Lock()
	defer c.mux.Unlock()
	cacheKey := attrKey + "=" + attrValue
	if dt, ok := c.cache[cacheKey]; ok {
		return dt, false, nil
	}
	dt, found, err := c.find(ctx, attrKey, attrValue)
	if err != nil {
		return dt, false, err
	}
	if !found {
		dt, err = candidate()
		if err != nil {
			return dt, false, err
		}
		dt, err = c.create(ctx, attrKey, dt)
		if err != nil {
			// another gateway may have created it in between
			var findErr error
			dt, found, findErr = c.find(ctx, attrKey, attrValue)
			if findErr != nil || !found {
				return dt, false, err
			}
		} else {
			created = true
		}
	}
	c.cache[cacheKey] = dt
	return dt, created, nil
}

// Find searches the device type with the given attribute without creating one.
func (c *Client) Find(ctx context.Context, attrKey, attrValue string) (models.DeviceType, bool, error) {
	return c.find(ctx, attrKey, attrValue)
}

func (c *Client) find(ctx context.Context, attrKey, attrValue string) (models.DeviceType, bool, error) {
	query := url.Values{
		"attr-keys":   {attrKey},
		"attr-values": {attrValue},
		"limit":       {"100"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseUrl+"/device-repository/v3/device-types?"+query.Encode(), nil)
	if err != nil {
		return models.DeviceType{}, false, err
	}
	var list []models.DeviceType
	if err = c.do(req, &list); err != nil {
		return models.DeviceType{}, false, fmt.Errorf("search device type: %w", err)
	}
	// attr-keys and attr-values filter independently, so the pair is checked here
	var result models.DeviceType
	found := false
	for _, dt := range list {
		for _, attr := range dt.Attributes {
			if attr.Key == attrKey && attr.Value == attrValue && (!found || dt.Id < result.Id) {
				result, found = dt, true
			}
		}
	}
	return result, found, nil
}

func (c *Client) create(ctx context.Context, distinctAttr string, dt models.DeviceType) (models.DeviceType, error) {
	body, err := json.Marshal(dt)
	if err != nil {
		return dt, err
	}
	endpoint := c.baseUrl + "/device-manager/device-types?" + url.Values{"distinct_attributes": {distinctAttr}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return dt, err
	}
	req.Header.Set("Content-Type", "application/json")
	var result models.DeviceType
	if err = c.do(req, &result); err != nil {
		return dt, fmt.Errorf("create device type: %w", err)
	}
	if result.Id == "" {
		return dt, fmt.Errorf("create device type: response without id")
	}
	return result, nil
}

func (c *Client) do(req *http.Request, result any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}
	return json.Unmarshal(body, result)
}
