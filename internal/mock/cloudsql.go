// Copyright 2021 Google LLC

// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at

//     https://www.apache.org/licenses/LICENSE-2.0

// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package mock

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"testing"
	"time"
)

// FakeCSQLInstance represents settings for a specific Cloud SQL instance.
//
// Use NewFakeCSQLInstance to instantiate.
type FakeCSQLInstance struct {
	project   string
	region    string
	name      string
	dbVersion string
	Key       *rsa.PrivateKey
	Cert      *x509.Certificate
}

// NewFakeCSQLInstance returns a CloudSQLInst object for configuring mocks.
func NewFakeCSQLInstance(project, region, name string) FakeCSQLInstance {
	// TODO: consider options for this?
	key, cert, err := generateCerts(project, name)
	if err != nil {
		panic(err)
	}

	return FakeCSQLInstance{
		project:   project,
		region:    region,
		name:      name,
		dbVersion: "POSTGRES_12", // default of no particular importance
		Key:       key,
		Cert:      cert,
	}
}

// GenerateCertWithCommonName produces a certificate signed by the Fake Cloud
// SQL instance's CA with the specified common name cn.
func GenerateCertWithCommonName(i FakeCSQLInstance, cn string) []byte {
	cert := &x509.Certificate{
		SerialNumber: &big.Int{},
		Subject: pkix.Name{
			CommonName: cn,
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(0, 0, 1),
		IsCA:                  true,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	badCert, err := x509.CreateCertificate(rand.Reader, cert, i.Cert, &i.Key.PublicKey, i.Key)
	if err != nil {
		panic(err)
	}
	return badCert
}

// generateCerts generates a private key, an X.509 certificate, and a TLS
// certificate for a particular fake Cloud SQL database instance.
func generateCerts(project, name string) (*rsa.PrivateKey, *x509.Certificate, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}

	cert := &x509.Certificate{
		SerialNumber: &big.Int{},
		Subject: pkix.Name{
			CommonName: fmt.Sprintf("%s:%s", project, name),
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(0, 0, 1),
		IsCA:                  true,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}

	return key, cert, nil
}

// StartServerProxy starts a fake server proxy and listens on the provided port
// on all interfaces, configured with TLS as specified by the FakeCSQLInstance.
// Callers should invoke the returned function to clean up all resources.
func StartServerProxy(t *testing.T, i FakeCSQLInstance) func() {
	certBytes, err := x509.CreateCertificate(
		rand.Reader, i.Cert, i.Cert, &i.Key.PublicKey, i.Key)
	if err != nil {
		t.Fatalf("failed to create certificate: %v", err)
	}

	caPEM := &bytes.Buffer{}
	pem.Encode(caPEM, &pem.Block{Type: "CERTIFICATE", Bytes: certBytes})

	caKeyPEM := &bytes.Buffer{}
	pem.Encode(caKeyPEM, &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(i.Key),
	})

	serverCert, err := tls.X509KeyPair(caPEM.Bytes(), caKeyPEM.Bytes())
	if err != nil {
		t.Fatalf("failed to create X.509 Key Pair: %v", err)
	}
	ln, err := tls.Listen("tcp", ":3307", &tls.Config{
		Certificates: []tls.Certificate{serverCert},
	})
	if err != nil {
		t.Fatalf("failed to start listener: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				conn, err := ln.Accept()
				if err != nil {
					t.Logf("fake server proxy will close listener after error: %v", err)
					return
				}
				conn.Write([]byte(i.name))
				conn.Close()
			}
		}
	}()
	return func() {
		ln.Close()
		cancel()
	}
}
