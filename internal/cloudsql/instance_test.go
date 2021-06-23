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

func TestConnectInfo(t *testing.T) {
	// define some test instance settings
	cn, _ := cloudsql.NewConnName("my-project:my-region:my-instance")
	inst := mock.NewFakeCSQLInstance("my-project", "my-region", "my-instance")

	// mock expected requests
	mc, url, cleanup := mock.HTTPClient(
		mock.InstanceGetSuccess(inst, 1),
		mock.CreateEphemeralSuccess(inst, 1),
	)
	defer func() {
		if err := cleanup(); err != nil {
			t.Fatalf("%v", err)
		}
	}()

	client, err := sqladmin.NewService(context.Background(), option.WithHTTPClient(mc), option.WithEndpoint(url))
	if err != nil {
		t.Fatalf("client init failed: %s", err)
	}

	// Step 0: Generate Keys
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate keys: %v", err)
	}

	im, err := cloudsql.NewInstance(cn, client, key, 30*time.Second)
	if err != nil {
		t.Fatalf("failed to initialize Instance: %v", err)
	}

	_, _, err = im.ConnectInfo(context.Background())
	if err != nil {
		t.Fatalf("failed to retrieve connect info: %v", err)
	}
}

func TestRefreshTimeout(t *testing.T) {
	ctx := context.Background()

	// mock expected requests
	mc, url, cleanup := mock.HTTPClient()
	defer func() {
		if err := cleanup(); err != nil {
			t.Fatalf("%v", err)
		}
	}()

	client, err := sqladmin.NewService(ctx, option.WithHTTPClient(mc), option.WithEndpoint(url))
	if err != nil {
		t.Fatalf("client init failed: %s", err)
	}
	// Step 0: Generate Keys
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate keys: %v", err)
	}

	// Use a timeout that should fail instantly
	im, err := cloudsql.NewInstance(cloudsql.ConnName{"my-project", "my-region", "my-instance"}, client, key, 0)
	if err != nil {
		t.Fatalf("failed to initialize Instance: %v", err)
	}

	_, _, err = im.ConnectInfo(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("failed to retrieve connect info: %v", err)
	}
}
