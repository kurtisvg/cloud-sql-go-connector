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

func testClient(project, region, instance string) (*sqladmin.Service, func() error, error) {
	inst := mock.NewFakeCSQLInstance(project, region, instance)
	mc, url, cleanup := mock.HTTPClient(
		mock.InstanceGetSuccess(inst, 1),
		mock.CreateEphemeralSuccess(inst, 1),
	)
	client, err := sqladmin.NewService(
		context.Background(),
		option.WithHTTPClient(mc),
		option.WithEndpoint(url),
	)
	return client, cleanup, err
}

func TestConnectInfo(t *testing.T) {
	client, cleanup, err := testClient("my-project", "my-region", "my-instance")
	if err != nil {
		t.Fatalf("failed to create test SQL admin service: %s", err)
	}
	defer cleanup()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate keys: %v", err)
	}

	cn, _ := cloudsql.NewConnName("my-project:my-region:my-instance")
	im := cloudsql.NewInstance(cn, client, key, 30*time.Second)

	_, _, err = im.ConnectInfo(context.Background())
	if err != nil {
		t.Fatalf("failed to retrieve connect info: %v", err)
	}
}

func TestRefreshTimeout(t *testing.T) {
	client, cleanup, err := testClient("my-project", "my-region", "my-instance")
	if err != nil {
		t.Fatalf("failed to create test SQL admin service: %s", err)
	}
	defer cleanup()

	// Step 0: Generate Keys
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate keys: %v", err)
	}

	cn, _ := cloudsql.NewConnName("my-project:my-region:my-instance")
	// Use a timeout that should fail instantly
	im := cloudsql.NewInstance(cn, client, key, 0)
	if err != nil {
		t.Fatalf("failed to initialize Instance: %v", err)
	}

	_, _, err = im.ConnectInfo(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("failed to retrieve connect info: %v", err)
	}
}
