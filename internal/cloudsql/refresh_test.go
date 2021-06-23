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
	"crypto/rand"
	"crypto/rsa"
	"testing"

	"cloud.google.com/cloudsqlconn/internal/mock"
	"google.golang.org/api/option"
	sqladmin "google.golang.org/api/sqladmin/v1beta4"
)

func TestFetchMetadata(t *testing.T) {
	ctx := context.Background()

	// define some test instance settings
	cn, err := NewConnName("my-proj:my-region:my-inst")
	if err != nil {
		t.Fatalf("%s", err)
	}
	inst := mock.NewFakeCSQLInstance(cn.Project, cn.Region, cn.Name)

	// mock expected requests
	mc, url, cleanup := mock.HTTPClient(
		mock.InstanceGetSuccess(inst, 1),
	)
	defer func() {
		if err := cleanup(); err != nil {
			t.Fatalf("%v", err)
		}
	}()

	client, err := sqladmin.NewService(ctx, option.WithHTTPClient(mc), option.WithEndpoint(url))
	if err != nil {
		t.Fatalf("client init failed: %s", err)
	}

	_, err = fetchMetadata(ctx, client, cn)
	if err != nil {
		t.Fatalf("%s", err)
	}
}
func TestFetchEphemeralCert(t *testing.T) {
	ctx := context.Background()

	// define some test instance settings
	cn, err := NewConnName("my-proj:my-region:my-inst")
	if err != nil {
		t.Fatalf("%s", err)
	}
	inst := mock.NewFakeCSQLInstance(cn.Project, cn.Region, cn.Name)

	// mock expected requests
	mc, url, cleanup := mock.HTTPClient(
		mock.CreateEphemeralSuccess(inst, 1),
	)
	defer func() {
		if err := cleanup(); err != nil {
			t.Fatalf("%v", err)
		}
	}()

	client, err := sqladmin.NewService(ctx, option.WithHTTPClient(mc), option.WithEndpoint(url))
	if err != nil {
		t.Fatalf("client init failed: %s", err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate keys: %v", err)
	}

	_, err = fetchEphemeralCert(ctx, client, cn, key)
	if err != nil {
		t.Fatalf("failed to fetch ephemeral cert: %v", err)
	}
}
