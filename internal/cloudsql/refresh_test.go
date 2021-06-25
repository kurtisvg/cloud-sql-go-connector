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

package cloudsql_test

import (
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"testing"
	"time"

	"cloud.google.com/cloudsqlconn/internal/cloudsql"
	"cloud.google.com/cloudsqlconn/internal/mock"
	"google.golang.org/api/option"
	sqladmin "google.golang.org/api/sqladmin/v1beta4"
)

func TestRefresh(t *testing.T) {
	wantIP := "127.0.0.1"
	wantExpiry := time.Now().Add(time.Hour).UTC().Round(time.Second)
	wantConnName := "my-project:my-region:my-instance"
	cn, _ := cloudsql.NewConnName(wantConnName)
	client, cleanup, err := mock.TestClient(
		cn,
		&sqladmin.DatabaseInstance{IpAddresses: []*sqladmin.IpMapping{{IpAddress: "127.0.0.1", Type: "PRIMARY"}}},
		wantExpiry,
	)
	if err != nil {
		t.Fatalf("failed to create test SQL admin service: %s", err)
	}
	defer cleanup()

	r := cloudsql.NewRefresher(time.Hour, 30*time.Second, 2, client)
	md, tlsCfg, gotExpiry, err := r.PerformRefresh(context.Background(), cn, mock.RSAKey)

	if err != nil {
		t.Fatalf("PerformRefresh unexpectedly failed with error: %v", err)
	}

	gotIP, ok := md.IPAddrs[cloudsql.PublicIP]
	if !ok {
		t.Fatalf("metadata IP addresses did not include public address")
	}
	if wantIP != gotIP {
		t.Fatalf("metadata IP mismatch, want = %v, got = %v", wantIP, gotIP)
	}
	if wantExpiry != gotExpiry {
		t.Fatalf("expiry mismatch, want = %v, got = %v", wantExpiry, gotExpiry)
	}

	if wantConnName != tlsCfg.ServerName {
		t.Fatalf("server name mismatch, want = %v, got = %v", wantConnName, tlsCfg.ServerName)
	}
}

func TestRefreshFailsFast(t *testing.T) {
	cn, _ := cloudsql.NewConnName("my-project:my-region:my-instance")
	client, cleanup, err := mock.TestClient(
		cn,
		&sqladmin.DatabaseInstance{
			IpAddresses: []*sqladmin.IpMapping{
				{IpAddress: "127.0.0.1", Type: "PRIMARY"},
				{IpAddress: "0.0.0.0", Type: "PRIVATE"},
			}},
		time.Now().Add(time.Hour),
	)
	if err != nil {
		t.Fatalf("failed to create test SQL admin service: %s", err)
	}
	defer cleanup()

	r := cloudsql.NewRefresher(time.Hour, 30*time.Second, 1, client)
	_, _, _, err = r.PerformRefresh(context.Background(), cn, mock.RSAKey)
	if err != nil {
		t.Fatalf("expected no error, got = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// context is canceled
	_, _, _, err = r.PerformRefresh(ctx, cn, mock.RSAKey)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled error, got = %v", err)
	}

	// force the rate limiter to throttle with a timed out context
	ctx, _ = context.WithTimeout(context.Background(), time.Millisecond)
	_, _, _, err = r.PerformRefresh(ctx, cn, mock.RSAKey)

	if !mock.ErrorContains(err, "throttled") {
		t.Fatalf("expected throttled error, got = %v", err)
	}
}

func invalidCertPEM() string {
	certPEM := &bytes.Buffer{}
	pem.Encode(certPEM, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: []byte("hello"), // woops no cert
	})
	return certPEM.String()
}

func TestRefreshWithFailedMetadataCall(t *testing.T) {
	cn, _ := cloudsql.NewConnName("my-project:my-region:my-instance")
	inst := mock.NewFakeCSQLInstance(cn.Project, cn.Region, cn.Name)

	testCases := []struct {
		req     *mock.Request
		wantErr string
		desc    string
	}{
		{
			req:     mock.CreateEphemeralSuccess(inst, 1), // not a metadata call
			wantErr: "failed to get instance",
			desc:    "When the Metadata call fails",
		},
		{
			req: mock.InstanceGetSuccessWithDatabase(inst, 1,
				&sqladmin.DatabaseInstance{Region: "some-other-region"}),
			wantErr: "region was mismatched",
			desc:    "When the region does not match",
		},
		{
			req: mock.InstanceGetSuccessWithDatabase(inst, 1,
				&sqladmin.DatabaseInstance{
					Region:      "my-region",
					BackendType: "NOT_SECOND_GEN",
				}),
			wantErr: "only Second Generation",
			desc:    "When the instance isn't Second generation",
		},
		{
			req: mock.InstanceGetSuccessWithDatabase(inst, 1,
				&sqladmin.DatabaseInstance{
					Region:      "my-region",
					BackendType: "SECOND_GEN",
					// No IP addresss
					IpAddresses: []*sqladmin.IpMapping{},
				}),
			wantErr: "no supported IP addresses",
			desc:    "When the instance has no supported IP addresses",
		},
		{
			req: mock.InstanceGetSuccessWithDatabase(inst, 1,
				&sqladmin.DatabaseInstance{
					Region:      "my-region",
					BackendType: "SECOND_GEN",
					IpAddresses: []*sqladmin.IpMapping{{IpAddress: "0.0.0.0", Type: "PRIMARY"}},
					// No ServerCaCert
					ServerCaCert: &sqladmin.SslCert{},
				}),
			wantErr: "failed to decode",
			desc:    "When the server cert does not decode",
		},
		{
			req: mock.InstanceGetSuccessWithDatabase(inst, 1,
				&sqladmin.DatabaseInstance{
					Region:       "my-region",
					BackendType:  "SECOND_GEN",
					IpAddresses:  []*sqladmin.IpMapping{{IpAddress: "0.0.0.0", Type: "PRIMARY"}},
					ServerCaCert: &sqladmin.SslCert{Cert: invalidCertPEM()},
				}),
			wantErr: "failed to parse",
			desc:    "When the cert is not a valid X.509 cert",
		},
	}
	for i, tc := range testCases {
		mc, url, cleanup := mock.HTTPClient(mock.CreateEphemeralSuccess(inst, 1), tc.req)
		client, err := sqladmin.NewService(
			context.Background(),
			option.WithHTTPClient(mc),
			option.WithEndpoint(url),
		)
		if err != nil {
			t.Fatalf("failed to create test SQL admin service: %s", err)
		}
		defer cleanup()

		r := cloudsql.NewRefresher(time.Hour, 30*time.Second, 1, client)
		_, _, _, err = r.PerformRefresh(context.Background(), cn, mock.RSAKey)

		if !mock.ErrorContains(err, tc.wantErr) {
			t.Errorf("[%v] PerformRefresh failed with unexpected error, want = %v, got = %v", i, tc.wantErr, err)
		}
	}
}

func TestRefreshWithFailedEphemeralCertCall(t *testing.T) {
	cn, _ := cloudsql.NewConnName("my-project:my-region:my-instance")
	inst := mock.NewFakeCSQLInstance(cn.Project, cn.Region, cn.Name)

	testCases := []struct {
		req     *mock.Request
		wantErr string
		desc    string
	}{
		{
			req:     mock.InstanceGetSuccess(inst, 1), // not an ephemeral cert call
			wantErr: "create failed",
			desc:    "When the CreateEphemeralCert call fails",
		},
	}
	for i, tc := range testCases {
		mc, url, cleanup := mock.HTTPClient(mock.InstanceGetSuccess(inst, 1), tc.req)
		client, err := sqladmin.NewService(
			context.Background(),
			option.WithHTTPClient(mc),
			option.WithEndpoint(url),
		)
		if err != nil {
			t.Fatalf("failed to create test SQL admin service: %s", err)
		}
		defer cleanup()

		r := cloudsql.NewRefresher(time.Hour, 30*time.Second, 1, client)
		_, _, _, err = r.PerformRefresh(context.Background(), cn, mock.RSAKey)

		if !mock.ErrorContains(err, tc.wantErr) {
			t.Errorf("[%v] PerformRefresh failed with unexpected error, want = %v, got = %v", i, tc.wantErr, err)
		}
	}
}

func TestRefreshBuildsTLSConfig(t *testing.T) {
	wantServerName := "my-project:my-region:my-instance"
	cn, _ := cloudsql.NewConnName(wantServerName)
	inst := mock.NewFakeCSQLInstance(cn.Project, cn.Region, cn.Name)
	certBytes := mock.CreateCertificate(inst)
	mc, url, cleanup := mock.HTTPClient(
		mock.InstanceGetSuccessWithDatabase(inst, 1,
			&sqladmin.DatabaseInstance{
				IpAddresses: []*sqladmin.IpMapping{
					{IpAddress: "127.0.0.1", Type: "PRIMARY"},
					{IpAddress: "0.0.0.0", Type: "PRIVATE"},
				},
				ServerCaCert: &sqladmin.SslCert{
					Cert: string(certBytes),
				},
			},
		),
		mock.CreateEphemeralSuccessWithExpiry(inst, 1, time.Now().Add(time.Hour)),
	)
	defer cleanup()
	client, err := sqladmin.NewService(
		context.Background(),
		option.WithHTTPClient(mc),
		option.WithEndpoint(url),
	)
	if err != nil {
		t.Fatalf("failed to create test SQL admin service: %s", err)
	}

	r := cloudsql.NewRefresher(time.Hour, 30*time.Second, 1, client)
	_, tlsCfg, _, err := r.PerformRefresh(context.Background(), cn, mock.RSAKey)
	if err != nil {
		t.Fatalf("expected no error, got = %v", err)
	}

	if wantServerName != tlsCfg.ServerName {
		t.Fatalf(
			"TLS config has incorrect server name, want = %v, got = %v",
			wantServerName, tlsCfg.ServerName,
		)
	}

	wantCertLen := 1
	if wantCertLen != len(tlsCfg.Certificates) {
		t.Fatalf(
			"TLS config has unexpected number of certificates, want = %v, got = %v",
			wantCertLen, len(tlsCfg.Certificates),
		)
	}

	wantInsecure := true
	if wantInsecure != tlsCfg.InsecureSkipVerify {
		t.Fatalf(
			"TLS config should skip verification, want = %v, got = %v",
			wantInsecure, tlsCfg.InsecureSkipVerify,
		)
	}

	if tlsCfg.RootCAs == nil {
		t.Fatal("TLS config should include RootCA, got nil")
	}

	verifyPeerCert := tlsCfg.VerifyPeerCertificate
	b, _ := pem.Decode(certBytes)
	err = verifyPeerCert([][]byte{b.Bytes}, nil)
	if err != nil {
		t.Fatalf("expected to verify peer cert, got error: %v", err)
	}

	err = verifyPeerCert(nil, nil)
	if !mock.ErrorContains(err, "no certificate") {
		t.Fatalf("expected verify peer cert to fail, got = %v", err)
	}

	err = verifyPeerCert([][]byte{[]byte("not a cert")}, nil)
	if !mock.ErrorContains(err, "x509.ParseCertificate(rawCerts[0])") {
		t.Fatalf("expected verify peer cert to fail on invalid cert, got = %v", err)
	}

	badCert := mock.GenerateCertWithCommonName(inst, "wrong:wrong")
	err = verifyPeerCert([][]byte{badCert}, nil)
	if !mock.ErrorContains(err, "certificate had CN") {
		t.Fatalf("expected common name mistmatch to error, got = %v", err)
	}

	other := mock.NewFakeCSQLInstance(cn.Project, cn.Region, cn.Name)
	certBytes = mock.CreateCertificate(other)
	b, _ = pem.Decode(certBytes)
	err = verifyPeerCert([][]byte{b.Bytes}, nil)
	if !mock.ErrorContains(err, "signed by unknown authority") {
		t.Fatalf("expected certificate verification to fail, got = %v", err)
	}
}
