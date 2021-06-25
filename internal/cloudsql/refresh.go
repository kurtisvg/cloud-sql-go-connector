// Copyright 2020 Google LLC

// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at

//     https://www.apache.org/licenses/LICENSE-2.0

// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cloudsql

import (
	"context"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"time"

	"golang.org/x/time/rate"
	sqladmin "google.golang.org/api/sqladmin/v1beta4"
)

const (
	PublicIP  = "PUBLIC"
	PrivateIP = "PRIVATE"
)

// Metadata contains information about a Cloud SQL instance needed to create connections.
type Metadata struct {
	// IPAddrs is the collection of IP addresses key on instance type (public or
	// private)
	IPAddrs map[string]string
	// ServerCaCert is the certificate for the server-side proxy.
	ServerCaCert *x509.Certificate
	// Version is the version of the database instance (e.g., POSTGRES_13)
	Version string
}

// fetchMetadata uses the Cloud SQL Admin APIs get method to retreive the information about a Cloud SQL instance
// that is used to create secure connections.
func fetchMetadata(ctx context.Context, client *sqladmin.Service, inst ConnName) (Metadata, error) {
	db, err := client.Instances.Get(inst.Project, inst.Name).Context(ctx).Do()
	if err != nil {
		return Metadata{}, fmt.Errorf("failed to get instance (%s): %w", inst, err)
	}

	// validate the instance is supported for authenticated connections
	if db.Region != inst.Region {
		return Metadata{}, fmt.Errorf("provided region was mismatched - got %s, want %s", inst.Region, db.Region)
	}
	if db.BackendType != "SECOND_GEN" {
		return Metadata{}, fmt.Errorf("unsupported instance - only Second Generation instances are supported")
	}

	// parse any ip addresses that might be used to connect
	ipAddrs := make(map[string]string)
	for _, ip := range db.IpAddresses {
		switch ip.Type {
		case "PRIMARY":
			ipAddrs[PublicIP] = ip.IpAddress
		case "PRIVATE":
			ipAddrs[PrivateIP] = ip.IpAddress
		}
	}
	if len(ipAddrs) == 0 {
		return Metadata{}, fmt.Errorf("cannot connect to instance - it has no supported IP addresses")
	}

	// parse the server-side CA certificate
	b, _ := pem.Decode([]byte(db.ServerCaCert.Cert))
	if b == nil {
		return Metadata{}, errors.New("failed to decode valid PEM cert")
	}
	cert, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		return Metadata{}, fmt.Errorf("failed to parse as x509 cert: %s", err)
	}

	m := Metadata{
		IPAddrs:      ipAddrs,
		ServerCaCert: cert,
		Version:      db.DatabaseVersion,
	}

	return m, nil
}

// fetchEphemeralCert uses the Cloud SQL Admin API's createEphemeral method to create a signed TLS
// certificate that authorized to connect via the Cloud SQL instance's serverside proxy. The cert
// if valid for approximately one hour.
func fetchEphemeralCert(ctx context.Context, client *sqladmin.Service, inst ConnName, key *rsa.PrivateKey) (tls.Certificate, error) {
	clientPubKey, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return tls.Certificate{}, err
	}

	req := sqladmin.SslCertsCreateEphemeralRequest{
		PublicKey: string(pem.EncodeToMemory(&pem.Block{Bytes: clientPubKey, Type: "RSA PUBLIC KEY"})),
	}
	resp, err := client.SslCerts.CreateEphemeral(inst.Project, inst.Name, &req).Context(ctx).Do()
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create failed: %w", err)
	}

	// parse the client cert
	b, _ := pem.Decode([]byte(resp.Cert))
	if b == nil {
		return tls.Certificate{}, errors.New("failed to decode valid PEM cert")
	}
	clientCert, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("failed to parse as x509 cert: %s", err)
	}

	tmpCert := tls.Certificate{
		Certificate: [][]byte{clientCert.Raw},
		PrivateKey:  key,
		Leaf:        clientCert,
	}
	return tmpCert, nil
}

// createTLSConfig returns a *tls.Config for connecting securely to the Cloud SQL instance.
func createTLSConfig(inst ConnName, m Metadata, cert tls.Certificate) *tls.Config {
	certs := x509.NewCertPool()
	certs.AddCert(m.ServerCaCert)

	cfg := &tls.Config{
		ServerName:   inst.String(),
		Certificates: []tls.Certificate{cert},
		RootCAs:      certs,
		// We need to set InsecureSkipVerify to true due to
		// https://github.com/GoogleCloudPlatform/cloudsql-proxy/issues/194
		// https://tip.golang.org/doc/go1.11#crypto/x509
		//
		// Since we have a secure channel to the Cloud SQL API which we use to retrieve the
		// certificates, we instead need to implement our own VerifyPeerCertificate function
		// that will verify that the certificate is OK.
		InsecureSkipVerify:    true,
		VerifyPeerCertificate: genVerifyPeerCertificateFunc(inst, certs),
	}
	return cfg
}

// genVerifyPeerCertificateFunc creates a VerifyPeerCertificate func that verifies that the peer
// certificate is in the cert pool. We need to define our own because of our sketchy non-standard
// CNs.
func genVerifyPeerCertificateFunc(cn ConnName, pool *x509.CertPool) func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return fmt.Errorf("no certificate to verify")
		}

		cert, err := x509.ParseCertificate(rawCerts[0])
		if err != nil {
			return fmt.Errorf("x509.ParseCertificate(rawCerts[0]) returned error: %v", err)
		}

		opts := x509.VerifyOptions{Roots: pool}
		if _, err = cert.Verify(opts); err != nil {
			return err
		}

		certInstanceName := fmt.Sprintf("%s:%s", cn.Project, cn.Name)
		if cert.Subject.CommonName != certInstanceName {
			return fmt.Errorf("certificate had CN %q, expected %q", cert.Subject.CommonName, certInstanceName)
		}
		return nil
	}
}

// NewRefresher creates a Refresher.
func NewRefresher(timeout time.Duration, interval time.Duration, burst int, svc *sqladmin.Service) Refresher {
	return Refresher{
		timeout:       timeout,
		clientLimiter: rate.NewLimiter(rate.Every(interval), burst),
		client:        svc,
	}
}

// Refresher manages the SQL Admin API access to instance metadata and to
// ephemeral certificates.
type Refresher struct {
	// timeout is the maximum amount of time a refresh operation should be allowed to take.
	timeout time.Duration

	clientLimiter *rate.Limiter
	client        *sqladmin.Service
}

// PerformRefresh immediately performs a full refresh operation using the Cloud SQL Admin API.
// TODO(enocom): This function should return two values, a refreshed thing and
// an error, where the tls.Config and expiry are folded into the metadata.
func (r Refresher) PerformRefresh(ctx context.Context, cn ConnName, k *rsa.PrivateKey) (Metadata, *tls.Config, time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	if ctx.Err() == context.Canceled {
		return Metadata{}, nil, time.Time{}, ctx.Err()
	}

	// avoid refreshing too often to try not to tax the SQL Admin API quotas
	err := r.clientLimiter.Wait(ctx)
	if err != nil {
		return Metadata{}, nil, time.Time{}, fmt.Errorf("refresh was throttled until context expired: %w", err)
	}

	// start async fetching the instance's metadata
	type mdRes struct {
		md  Metadata
		err error
	}
	mdC := make(chan mdRes, 1)
	go func() {
		defer close(mdC)
		md, err := fetchMetadata(ctx, r.client, cn)
		mdC <- mdRes{md, err}
	}()

	// start async fetching the certs
	type ecRes struct {
		ec  tls.Certificate
		err error
	}
	ecC := make(chan ecRes, 1)
	go func() {
		defer close(ecC)
		ec, err := fetchEphemeralCert(ctx, r.client, cn, k)
		ecC <- ecRes{ec, err}
	}()

	// wait for the results of each operations
	var md Metadata
	select {
	case r := <-mdC:
		if r.err != nil {
			return md, nil, time.Time{}, fmt.Errorf("fetch metadata failed: %w", r.err)
		}
		md = r.md
	case <-ctx.Done():
		return md, nil, time.Time{}, fmt.Errorf("refresh failed: %w", ctx.Err())
	}
	var ec tls.Certificate
	select {
	case r := <-ecC:
		if r.err != nil {
			return md, nil, time.Time{}, fmt.Errorf("fetch ephemeral cert failed: %w", r.err)
		}
		ec = r.ec
	case <-ctx.Done():
		return md, nil, time.Time{}, fmt.Errorf("refresh failed: %w", ctx.Err())
	}

	c := createTLSConfig(cn, md, ec)
	// This should never not be the case, but we check to avoid a potential nil-pointer
	expiry := time.Time{}
	if len(c.Certificates) > 0 {
		expiry = c.Certificates[0].Leaf.NotAfter
	}
	return md, c, expiry, nil
}
