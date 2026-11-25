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

// Package connectlife talks to the ConnectLife cloud (Hisense, Gorenje, ASKO appliances).
//
// The protocol follows the description of the ConnectLife app API as used by the
// Home Assistant integration connectlife-ha: login at Gigya, OAuth code exchange at
// oauth.hijuconn.com, then signed requests to the HijuConn gateway. The code is an
// independent implementation; nothing is taken over from that project.
package connectlife

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	gigyaLoginUrl  = "https://accounts.eu1.gigya.com/accounts.login"
	gigyaJwtUrl    = "https://accounts.eu1.gigya.com/accounts.getJWT"
	oauthAuthorize = "https://oauth.hijuconn.com/oauth/authorize"
	oauthToken     = "https://oauth.hijuconn.com/oauth/token"
	oauthClientId  = "5065059336212"
	oauthRedirect  = "https://api.connectlife.io/swagger/oauth2-redirect.html"

	gatewayBaseUrl   = "https://clife-eu-gateway.hijuconn.com"
	deviceListPath   = "/clife-svc/pu/get_device_status_list"
	propertySetPath  = "/device/pu/property/set"
	propertyListPath = "/clife-svc/get_property_list"

	gatewayAppId     = "47110565134383"
	gatewayLanguage  = "12"
	gatewayTimezone  = "1.0"
	gatewayVersion   = "5.0"
	gatewayUserAgent = "mgw-connectlife-dc"

	errInvalidAccessToken = "100026"
	errRandStrCheckFailed = "101005"

	// tokenRenewMargin renews the access token before it expires.
	tokenRenewMargin = 90 * time.Second
)

type Credentials struct {
	Username          string
	Password          string
	GigyaApiKey       string
	OAuthClientSecret string
	AppSecret         string
	SignSalt          string
}

type Client struct {
	cred       Credentials
	http       *http.Client
	signer     *signer
	urls       endpoints
	mux        sync.Mutex
	access     string
	refresh    string
	expiration time.Time
}

// endpoints is a field so tests can point the client at a local server.
type endpoints struct {
	gigyaLogin, gigyaJwt, authorize, token, gateway string
}

var defaultEndpoints = endpoints{
	gigyaLogin: gigyaLoginUrl,
	gigyaJwt:   gigyaJwtUrl,
	authorize:  oauthAuthorize,
	token:      oauthToken,
	gateway:    gatewayBaseUrl,
}

func New(cred Credentials) (*Client, error) {
	missing := []string{}
	for name, value := range map[string]string{
		"username":            cred.Username,
		"password":            cred.Password,
		"gigya api key":       cred.GigyaApiKey,
		"oauth client secret": cred.OAuthClientSecret,
		"app secret":          cred.AppSecret,
		"sign salt":           cred.SignSalt,
	} {
		if value == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("missing connectlife configuration: %s", strings.Join(missing, ", "))
	}
	s, err := newSigner(gatewayPublicKey, cred.SignSalt)
	if err != nil {
		return nil, err
	}
	return &Client{
		cred:   cred,
		http:   &http.Client{Timeout: 30 * time.Second},
		signer: s,
		urls:   defaultEndpoints,
	}, nil
}

// GetAppliances returns all appliances of the account with their current status.
func (c *Client) GetAppliances(ctx context.Context) ([]Appliance, error) {
	raw, err := c.gateway(ctx, http.MethodGet, deviceListPath, nil)
	if err != nil {
		return nil, err
	}
	var list deviceListResponse
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err = dec.Decode(&list); err != nil {
		return nil, fmt.Errorf("unable to decode device list: %w", err)
	}
	if list.DeviceList == nil {
		return nil, errors.New("gateway response without deviceList")
	}
	result := make([]Appliance, 0, len(list.DeviceList))
	for _, a := range list.DeviceList {
		if a.DeviceId != "" {
			result = append(result, a)
		}
	}
	return result, nil
}

// SetProperties writes properties of one appliance. The gateway expects every value as string.
func (c *Client) SetProperties(ctx context.Context, puid string, properties map[string]string) error {
	_, err := c.gateway(ctx, http.MethodPost, propertySetPath, map[string]any{
		"puid":       puid,
		"properties": properties,
	})
	return err
}

// GetPropertyList returns the property metadata the cloud keeps for an appliance model.
// The shape of the response is not documented; it is returned unparsed.
func (c *Client) GetPropertyList(ctx context.Context, deviceTypeCode, deviceFeatureCode string) (json.RawMessage, error) {
	return c.gateway(ctx, http.MethodGet, propertyListPath, map[string]any{
		"deviceTypeCode":    deviceTypeCode,
		"deviceFeatureCode": deviceFeatureCode,
	})
}

// GetProperties returns the properties of an appliance model, also while the appliance is offline.
func (c *Client) GetProperties(ctx context.Context, deviceTypeCode, deviceFeatureCode string) ([]PropertyInfo, error) {
	raw, err := c.GetPropertyList(ctx, deviceTypeCode, deviceFeatureCode)
	if err != nil {
		return nil, err
	}
	var list propertyListResponse
	if err = json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("unable to decode property list: %w", err)
	}
	return list.Properties, nil
}

func (c *Client) gateway(ctx context.Context, method, path string, payload map[string]any) (json.RawMessage, error) {
	reauth, renewRandStr := true, true
	for {
		raw, code, err := c.gatewayOnce(ctx, method, path, payload)
		switch {
		case err == nil:
			return raw, nil
		case code == errInvalidAccessToken && reauth:
			reauth = false
			c.resetToken()
		case code == errRandStrCheckFailed && renewRandStr:
			renewRandStr = false
		default:
			return nil, err
		}
	}
}

func (c *Client) gatewayOnce(ctx context.Context, method, path string, payload map[string]any) (raw json.RawMessage, errCode string, err error) {
	token, err := c.accessToken(ctx)
	if err != nil {
		return nil, "", err
	}
	data, err := c.requestData(token, payload)
	if err != nil {
		return nil, "", err
	}
	var req *http.Request
	if method == http.MethodGet {
		query := url.Values{}
		for k, v := range data {
			query.Set(k, fmt.Sprint(v))
		}
		req, err = http.NewRequestWithContext(ctx, method, c.urls.gateway+path+"?"+query.Encode(), nil)
	} else {
		body, encErr := compactJson(data)
		if encErr != nil {
			return nil, "", encErr
		}
		req, err = http.NewRequestWithContext(ctx, method, c.urls.gateway+path, bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
		}
	}
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", gatewayUserAgent)
	body, err := c.do(req)
	if err != nil {
		return nil, "", fmt.Errorf("connectlife gateway %s: %w", path, err)
	}
	var env gatewayEnvelope
	if err = json.Unmarshal(body, &env); err != nil || len(env.Response) == 0 || env.Response[0] != '{' {
		return nil, "", fmt.Errorf("connectlife gateway %s: response without object 'response'", path)
	}
	var status gatewayResponse
	if err = json.Unmarshal(env.Response, &status); err != nil {
		return nil, "", fmt.Errorf("connectlife gateway %s: %w", path, err)
	}
	if status.ResultCode == nil || fmt.Sprint(status.ResultCode) == "0" {
		return env.Response, "", nil
	}
	errCode = numberString(status.ErrorCode)
	return nil, errCode, fmt.Errorf("connectlife gateway %s: error %s: %s", path, errCode, status.ErrorDesc)
}

func (c *Client) requestData(token string, payload map[string]any) (map[string]any, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	data := map[string]any{
		"accessToken": token,
		"appId":       gatewayAppId,
		"appSecret":   c.cred.AppSecret,
		"languageId":  gatewayLanguage,
		"randStr":     hex.EncodeToString(nonce),
		"timeStamp":   fmt.Sprint(time.Now().UnixMilli()),
		"timezone":    gatewayTimezone,
		"version":     gatewayVersion,
	}
	for k, v := range payload {
		data[k] = v
	}
	sign, err := c.signer.sign(data)
	if err != nil {
		return nil, err
	}
	data["sign"] = sign
	return data, nil
}

func (c *Client) resetToken() {
	c.mux.Lock()
	defer c.mux.Unlock()
	c.access = ""
	c.refresh = ""
	c.expiration = time.Time{}
}

func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mux.Lock()
	defer c.mux.Unlock()
	if c.access != "" && time.Now().Before(c.expiration) {
		return c.access, nil
	}
	if c.refresh != "" {
		err := c.tokenRequest(ctx, url.Values{
			"grant_type":    {"refresh_token"},
			"refresh_token": {c.refresh},
		})
		if err == nil {
			return c.access, nil
		}
		c.refresh = ""
	}
	if err := c.login(ctx); err != nil {
		return "", err
	}
	return c.access, nil
}

// login runs the full flow. The caller holds c.mux.
func (c *Client) login(ctx context.Context) error {
	var login struct {
		gigyaError
		UID         string `json:"UID"`
		SessionInfo struct {
			CookieValue string `json:"cookieValue"`
		} `json:"sessionInfo"`
	}
	err := c.postForm(ctx, c.urls.gigyaLogin, url.Values{
		"loginID":  {c.cred.Username},
		"password": {c.cred.Password},
		"APIKey":   {c.cred.GigyaApiKey},
	}, &login)
	if err != nil {
		return fmt.Errorf("connectlife login: %w", err)
	}
	if err = login.err(); err != nil {
		return fmt.Errorf("connectlife login: %w", err)
	}
	if login.UID == "" || login.SessionInfo.CookieValue == "" {
		return errors.New("connectlife login: response without UID or sessionInfo.cookieValue")
	}

	var jwt struct {
		gigyaError
		IdToken string `json:"id_token"`
	}
	err = c.postForm(ctx, c.urls.gigyaJwt, url.Values{
		"APIKey":      {c.cred.GigyaApiKey},
		"login_token": {login.SessionInfo.CookieValue},
	}, &jwt)
	if err == nil {
		err = jwt.err()
	}
	if err != nil {
		return fmt.Errorf("connectlife jwt: %w", err)
	}
	if jwt.IdToken == "" {
		return errors.New("connectlife jwt: response without id_token")
	}

	body, err := json.Marshal(map[string]string{
		"client_id":     oauthClientId,
		"redirect_uri":  oauthRedirect,
		"idToken":       jwt.IdToken,
		"response_type": "code",
		"thirdType":     "CDC",
		"thirdClientId": login.UID,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.urls.authorize, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	respBody, err := c.do(req)
	if err != nil {
		return fmt.Errorf("connectlife authorize: %w", err)
	}
	var authorize struct {
		Code string `json:"code"`
	}
	if err = json.Unmarshal(respBody, &authorize); err != nil || authorize.Code == "" {
		return errors.New("connectlife authorize: response without code")
	}

	return c.tokenRequest(ctx, url.Values{
		"grant_type": {"authorization_code"},
		"code":       {authorize.Code},
	})
}

// tokenRequest exchanges a code or refresh token. The caller holds c.mux.
func (c *Client) tokenRequest(ctx context.Context, values url.Values) error {
	values.Set("client_id", oauthClientId)
	values.Set("client_secret", c.cred.OAuthClientSecret)
	values.Set("redirect_uri", oauthRedirect)
	var token tokenResponse
	if err := c.postForm(ctx, c.urls.token, values, &token); err != nil {
		return fmt.Errorf("connectlife token: %w", err)
	}
	if token.AccessToken == "" || token.ExpiresIn == 0 {
		return errors.New("connectlife token: response without access_token or expires_in")
	}
	c.access = token.AccessToken
	c.expiration = time.Now().Add(time.Duration(token.ExpiresIn)*time.Second - tokenRenewMargin)
	if token.RefreshToken != "" {
		c.refresh = token.RefreshToken
	}
	return nil
}

type gigyaError struct {
	ErrorCode    FlexInt `json:"errorCode"`
	ErrorMessage string  `json:"errorMessage"`
	ErrorDetails string  `json:"errorDetails"`
}

// gigyaPendingRegistration means the account has not accepted the current terms in the ConnectLife app.
const gigyaPendingRegistration = 206001

func (e gigyaError) err() error {
	if e.ErrorCode == 0 && e.ErrorMessage == "" && e.ErrorDetails == "" {
		return nil
	}
	if e.ErrorCode == gigyaPendingRegistration {
		return errors.New("account must accept the terms in the ConnectLife app first")
	}
	return fmt.Errorf("gigya error %d: %s %s", e.ErrorCode, e.ErrorMessage, e.ErrorDetails)
}

func (c *Client) postForm(ctx context.Context, endpoint string, values url.Values, result any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	body, err := c.do(req)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, result)
}

// do returns the body of a 200 response. Errors carry neither query nor body, because both may hold credentials.
func (c *Client) do(req *http.Request) ([]byte, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		// a url.Error repeats the url, whose query carries the access token and app secret
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return nil, fmt.Errorf("%s %s%s: %w", urlErr.Op, req.URL.Host, req.URL.Path, urlErr.Err)
		}
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return body, nil
}

func numberString(v any) string {
	switch n := v.(type) {
	case nil:
		return ""
	case float64:
		return fmt.Sprintf("%.0f", n)
	default:
		return fmt.Sprint(n)
	}
}
