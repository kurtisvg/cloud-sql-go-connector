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
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"testing"
	"time"

	"cloud.google.com/cloudsqlconn/internal/cloudsql"
	"cloud.google.com/cloudsqlconn/internal/mock"
	"google.golang.org/api/option"
	sqladmin "google.golang.org/api/sqladmin/v1beta4"
)

func TestParseConnName(t *testing.T) {
	tests := []struct {
		name string
		want cloudsql.ConnName
	}{
		{
			"project:region:instance",
			cloudsql.ConnName{"project", "region", "instance"},
		},
		{
			"google.com:project:region:instance",
			cloudsql.ConnName{"google.com:project", "region", "instance"},
		},
		{
			"project:instance", // missing region
			cloudsql.ConnName{},
		},
	}

	for _, tc := range tests {
		c, err := cloudsql.NewConnName(tc.name)
		if err != nil && tc.want != (cloudsql.ConnName{}) {
			t.Errorf("unexpected error: %e", err)
		}
		if c != tc.want {
			t.Errorf("NewConnName(%s) failed: want %v, got %v", tc.name, tc.want, err)
		}
	}
}

func testClient(cn cloudsql.ConnName, addrs []mock.IPMapping) (*sqladmin.Service, func() error, error) {
	inst := mock.NewFakeCSQLInstance(cn.Project, cn.Region, cn.Name)
	mc, url, cleanup := mock.HTTPClient(
		mock.InstanceGetSuccessWithOpts(inst, 1, addrs),
		mock.CreateEphemeralSuccess(inst, 1),
	)
	client, err := sqladmin.NewService(
		context.Background(),
		option.WithHTTPClient(mc),
		option.WithEndpoint(url),
	)
	return client, cleanup, err
}

func rsaKey() *rsa.PrivateKey {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err) // unexpected, so just panic if it happens
	}
	return key
}

func TestConnectInfo(t *testing.T) {
	wantAddr := "0.0.0.0"
	cn, _ := cloudsql.NewConnName("my-project:my-region:my-instance")
	client, cleanup, err := testClient(
		cn,
		[]mock.IPMapping{{IPAddr: wantAddr, Type: "PRIMARY"}},
	)
	if err != nil {
		t.Fatalf("failed to create test SQL admin service: %s", err)
	}
	defer cleanup()

	i := cloudsql.NewInstance(cn, client, rsaKey(), 30*time.Second)

	gotAddr, gotTLSCfg, err := i.ConnectInfo(context.Background(), cloudsql.PublicIP)
	if err != nil {
		t.Fatalf("failed to retrieve connect info: %v", err)
	}

	if gotAddr != wantAddr {
		t.Fatalf(
			"ConnectInfo returned unexpected IP address, want = %v, got = %v",
			wantAddr, gotAddr,
		)
	}

	wantServerName := "my-project:my-region:my-instance"
	if gotTLSCfg.ServerName != wantServerName {
		t.Fatalf(
			"ConnectInfo return unexpected server name in TLS Config, want = %v, got = %v",
			wantServerName, gotTLSCfg.ServerName,
		)
	}
}

func TestConnectInfoErrors(t *testing.T) {
	cn, _ := cloudsql.NewConnName("my-project:my-region:my-instance")
	client, cleanup, err := testClient(
		cn,
		[]mock.IPMapping{{IPAddr: "127.0.0.1", Type: cloudsql.PublicIP}},
	)
	if err != nil {
		t.Fatalf("failed to create test SQL admin service: %s", err)
	}
	defer cleanup()

	// Use a timeout that should fail instantly
	i := cloudsql.NewInstance(cn, client, rsaKey(), 0)
	if err != nil {
		t.Fatalf("failed to initialize Instance: %v", err)
	}

	_, _, err = i.ConnectInfo(context.Background(), cloudsql.PublicIP)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("failed to retrieve connect info: %v", err)
	}

	// when client asks for wrong IP address type
	gotAddr, _, err := i.ConnectInfo(context.Background(), cloudsql.PrivateIP)
	if err == nil {
		t.Fatalf("expected ConnectInfo to fail but returned IP address = %v", gotAddr)
	}
}
