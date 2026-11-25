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
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testKey(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func TestEmbeddedPublicKeyParses(t *testing.T) {
	if _, err := newSigner(gatewayPublicKey, "salt"); err != nil {
		t.Fatal(err)
	}
}

func TestSignPlaintext(t *testing.T) {
	s := &signer{salt: "SALT"}
	actual, err := s.signPlaintext(map[string]any{
		"puid":        "p1",
		"accessToken": "tok",
		"sign":        "ignored",
		"properties":  map[string]string{"b": "2", "a": "<1>"},
	})
	if err != nil {
		t.Fatal(err)
	}
	expected := `accessToken=tok&properties={"a":"<1>","b":"2"}&puid=p1SALT`
	if actual != expected {
		t.Errorf("got %q, want %q", actual, expected)
	}
}

// verifySign checks a signature the way the gateway does: decrypt and compare with the SHA-256 of the fields.
func verifySign(t *testing.T, key *rsa.PrivateKey, salt string, data map[string]any) {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(data["sign"].(string))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := rsa.DecryptPKCS1v15(rand.Reader, key, raw)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := (&signer{salt: salt}).signPlaintext(data)
	if err != nil {
		t.Fatal(err)
	}
	expected := sha256.Sum256([]byte(plaintext))
	if string(digest) != string(expected[:]) {
		t.Error("signature does not match request fields")
	}
}

func TestClientFlow(t *testing.T) {
	key, pub := testKey(t)
	const salt = "SALT"
	var logins, sets atomic.Int32
	var rejectFirstToken atomic.Bool
	rejectFirstToken.Store(true)

	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		logins.Add(1)
		_ = r.ParseForm()
		if r.Form.Get("loginID") != "user" || r.Form.Get("password") != "pw" || r.Form.Get("APIKey") != "gigya" {
			_ = json.NewEncoder(w).Encode(map[string]any{"errorCode": 403042, "errorMessage": "Invalid LoginID"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"errorCode": 0, "UID": "uid1", "sessionInfo": map[string]any{"cookieValue": "cookie1"}})
	})
	mux.HandleFunc("/jwt", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("login_token") != "cookie1" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"errorCode": 0, "id_token": "idtoken1"})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["idToken"] != "idtoken1" || body["thirdClientId"] != "uid1" || body["client_id"] != oauthClientId {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "code1"})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("client_secret") != "oauth" || r.Form.Get("code") != "code1" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		token := "access2"
		if rejectFirstToken.Load() {
			token = "access1"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": token, "expires_in": 3600, "refresh_token": "refresh1"})
	})
	mux.HandleFunc(deviceListPath, func(w http.ResponseWriter, r *http.Request) {
		data := map[string]any{}
		for k, v := range r.URL.Query() {
			data[k] = v[0]
		}
		verifySign(t, key, salt, data)
		if data["appSecret"] != "app" || data["appId"] != gatewayAppId {
			t.Error("missing app credentials")
		}
		if data["accessToken"] == "access1" {
			// first token is rejected once, the client must log in again
			rejectFirstToken.Store(false)
			_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"resultCode": 1, "errorCode": 100026, "errorDesc": "invalid token"}})
			return
		}
		_, _ = io.WriteString(w, `{"response":{"resultCode":0,"deviceList":[
			{"deviceId":"d1","puid":"p1","deviceTypeCode":"030","deviceFeatureCode":"1wk080027e0w","deviceTypeName":"Tumble dryer","offlineState":1,"statusList":{"t_power":"1","t_remaining":"95"}},
			{"puid":"without-device-id"}]}}`)
	})
	mux.HandleFunc(propertySetPath, func(w http.ResponseWriter, r *http.Request) {
		sets.Add(1)
		var data map[string]any
		_ = json.NewDecoder(r.Body).Decode(&data)
		props := map[string]string{}
		for k, v := range data["properties"].(map[string]any) {
			props[k] = v.(string)
		}
		data["properties"] = props
		verifySign(t, key, salt, data)
		if data["puid"] != "p1" || props["t_power"] != "0" {
			t.Errorf("unexpected payload %v", data)
		}
		_, _ = io.WriteString(w, `{"response":{"resultCode":"0"}}`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	s, err := newSigner(pub, salt)
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(Credentials{Username: "user", Password: "pw", GigyaApiKey: "gigya", OAuthClientSecret: "oauth", AppSecret: "app", SignSalt: salt})
	if err != nil {
		t.Fatal(err)
	}
	c.signer = s
	c.urls = endpoints{gigyaLogin: server.URL + "/login", gigyaJwt: server.URL + "/jwt", authorize: server.URL + "/authorize", token: server.URL + "/token", gateway: server.URL}

	appliances, err := c.GetAppliances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if logins.Load() != 2 {
		t.Errorf("expected a second login after the rejected token, got %d logins", logins.Load())
	}
	if len(appliances) != 1 || appliances[0].TypeKey() != "030-1wk080027e0w" || !appliances[0].Online() {
		t.Fatalf("unexpected appliances %+v", appliances)
	}
	if v, ok := appliances[0].StatusList["t_remaining"].(string); !ok || v != "95" {
		t.Errorf("unexpected status value %#v", appliances[0].StatusList["t_remaining"])
	}

	if err = c.SetProperties(context.Background(), "p1", map[string]string{"t_power": "0"}); err != nil {
		t.Fatal(err)
	}
	if sets.Load() != 1 || logins.Load() != 2 {
		t.Errorf("unexpected calls: sets %d logins %d", sets.Load(), logins.Load())
	}
}

func TestLoginError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"errorCode": 206001, "errorMessage": "Account Pending Registration"})
	}))
	defer server.Close()
	c, err := New(Credentials{Username: "u", Password: "p", GigyaApiKey: "g", OAuthClientSecret: "o", AppSecret: "a", SignSalt: "s"})
	if err != nil {
		t.Fatal(err)
	}
	c.urls = endpoints{gigyaLogin: server.URL, gateway: server.URL}
	_, err = c.GetAppliances(context.Background())
	if err == nil || err.Error() != "connectlife login: account must accept the terms in the ConnectLife app first" {
		t.Errorf("unexpected error %v", err)
	}
}

func TestNewRequiresCredentials(t *testing.T) {
	_, err := New(Credentials{Username: "u"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestTransportErrorHidesQuery(t *testing.T) {
	c, err := New(Credentials{Username: "u", Password: "p", GigyaApiKey: "g", OAuthClientSecret: "o", AppSecret: "APPSECRET", SignSalt: "s"})
	if err != nil {
		t.Fatal(err)
	}
	c.access = "TOKEN123"
	c.expiration = time.Now().Add(time.Hour)
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	c.urls = endpoints{gateway: closed.URL}
	_, err = c.GetAppliances(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "TOKEN123") || strings.Contains(err.Error(), "APPSECRET") {
		t.Errorf("error leaks credentials: %v", err)
	}
}
