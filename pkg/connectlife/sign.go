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
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// gatewayPublicKey is the RSA key of the ConnectLife app that encrypts the request signature.
const gatewayPublicKey = `-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAyyWrNG6q475HIHu7sMVu
vHof6vlgPeixmxa4EL/UsvVvHPz33NnWoQetQqit9TBNzUjMXw0KlY9PXM4iqHUU
U+dSyNDq1jZWIiJ2C2FccppswJtIKL3NRMFvT9PFh6NlP/4FUcQKojgKFbF7Kacc
JPKYHlwaO7qgoIjLxAHlSOXGpucJcOkPzT2EqsSVnW8sn8kenvNmghXDayhgxsh6
AyxK4kehJplEnmX/iYCfNoFXknGcLqFWYccgBz3fybvx30C/0IgU1980L8QsUAv5
esZmN8ugnbRgLRxKRlkQQLxQAiZMZdKTAx665YflT3YMHJvEFE8c2XFgoxHzSMc4
BwIDAQAB
-----END PUBLIC KEY-----
`

type signer struct {
	key  *rsa.PublicKey
	salt string
}

func newSigner(publicKeyPem string, salt string) (*signer, error) {
	block, _ := pem.Decode([]byte(publicKeyPem))
	if block == nil {
		return nil, errors.New("invalid gateway public key")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("invalid gateway public key: %w", err)
	}
	key, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("gateway public key is not an RSA key")
	}
	return &signer{key: key, salt: salt}, nil
}

// signPlaintext is the string the gateway hashes: all fields except sign, sorted by key,
// joined as key=value with "&", objects as compact JSON, followed by the salt.
func (s *signer) signPlaintext(data map[string]any) (string, error) {
	keys := make([]string, 0, len(data))
	for k := range data {
		if k != "sign" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		value, err := signValue(data[k])
		if err != nil {
			return "", err
		}
		parts = append(parts, k+"="+value)
	}
	return strings.Join(parts, "&") + s.salt, nil
}

// sign returns base64(RSA-PKCS1v15(SHA-256(plaintext))).
func (s *signer) sign(data map[string]any) (string, error) {
	plaintext, err := s.signPlaintext(data)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(plaintext))
	encrypted, err := rsa.EncryptPKCS1v15(rand.Reader, s.key, digest[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(encrypted), nil
}

func signValue(v any) (string, error) {
	switch v.(type) {
	case map[string]any, map[string]string, []any, []string:
		b, err := compactJson(v)
		return string(b), err
	default:
		return fmt.Sprint(v), nil
	}
}

// compactJson encodes without HTML escaping, so the signed text and the sent body are identical.
func compactJson(v any) ([]byte, error) {
	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
