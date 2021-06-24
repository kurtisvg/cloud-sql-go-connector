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

package cloudsqlconn

import (
	"context"
	"errors"
	"io/ioutil"
	"net/http"
	"testing"
	"time"

	"cloud.google.com/cloudsqlconn/internal/mock"
	"google.golang.org/api/option"
	sqladmin "google.golang.org/api/sqladmin/v1beta4"
)

// replaceSQLAdminClient patches the Dialer's SQL Admin API client with one that
// uses the provided HTTP client and endpoint.
func newMockService(client *http.Client, endpoint string) *sqladmin.Service {
	svc, err := sqladmin.NewService(
		context.Background(),
		option.WithHTTPClient(client),
		option.WithEndpoint(endpoint),
	)
	if err != nil {
		panic(err)
	}
	return svc
}

func TestDialerCanConnectToInstance(t *testing.T) {
	inst := mock.NewFakeCSQLInstance("my-project", "my-region", "my-instance")
	mc, url, cleanup := mock.HTTPClient(
		mock.InstanceGetSuccess(inst, 1),
		mock.CreateEphemeralSuccess(inst, 1),
	)
	stop := mock.StartServerProxy(t, inst)
	defer func() {
		stop()
		if err := cleanup(); err != nil {
			t.Fatalf("%v", err)
		}
	}()

	d, err := NewDialer(context.Background(), WithDefaultDialOptions(WithPublicIP()))
	if err != nil {
		t.Fatalf("expected NewDialer to succeed, but got error: %v", err)
	}
	d.sqladmin = newMockService(mc, url)

	conn, err := d.Dial(context.Background(), "my-project:my-region:my-instance", WithPublicIP())
	if err != nil {
		t.Fatalf("expected Dial to succeed, but got error: %v", err)
	}
	defer conn.Close()

	data, err := ioutil.ReadAll(conn)
	if err != nil {
		t.Fatalf("expected ReadAll to succeed, got error %v", err)
	}
	if string(data) != "my-instance" {
		t.Fatalf("expected known response from the server, but got %v", string(data))
	}
}

func TestDialerInstantiationErrors(t *testing.T) {
	_, err := NewDialer(context.Background(), WithCredentialsFile("bogus-file.json"))
	if err == nil {
		t.Fatalf("expected NewDialer to return error, but got none.")
	}
}

func TestDialWithAdminAPIErrors(t *testing.T) {
	inst := mock.NewFakeCSQLInstance("my-project", "my-region", "my-instance")
	mc, url, cleanup := mock.HTTPClient()
	stop := mock.StartServerProxy(t, inst)
	defer func() {
		stop()
		if err := cleanup(); err != nil {
			t.Fatalf("%v", err)
		}
	}()

	d, err := NewDialer(context.Background(), WithDefaultDialOptions(WithPublicIP()))
	if err != nil {
		t.Fatalf("expected NewDialer to succeed, but got error: %v", err)
	}
	d.sqladmin = newMockService(mc, url)

	// instance name is bad
	_, err = d.Dial(context.Background(), "bad-instance-name")
	if !mock.ErrorContains(err, "invalid instance") {
		t.Fatalf("expected Dial to fail with bad instance name, but it succeeded.")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// context is canceled
	_, err = d.Dial(ctx, "my-project:my-region:my-instance")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected Dial to fail with canceled context, but it succeeded.")
	}

	// failed to retrieve metadata or ephemeral cert (not registered in the mock)
	_, err = d.Dial(context.Background(), "my-project:my-region:my-instance")
	if !mock.ErrorContains(err, "fetch metadata failed") {
		t.Fatalf("expected Dial to fail with missing metadata")
	}
}

func TestDialWithConfigurationErrors(t *testing.T) {
	inst := mock.NewFakeCSQLInstance("my-project", "my-region", "my-instance")
	mc, url, cleanup := mock.HTTPClient(
		mock.InstanceGetSuccess(inst, 2),
		mock.CreateEphemeralSuccess(inst, 2),
	)
	d, err := NewDialer(context.Background(), WithDefaultDialOptions(WithPublicIP()))
	if err != nil {
		t.Fatalf("expected NewDialer to succeed, but got error: %v", err)
	}
	d.sqladmin = newMockService(mc, url)
	defer func() {
		if err := cleanup(); err != nil {
			t.Fatalf("%v", err)
		}
	}()

	// when failing to find private IP for public-only instance
	_, err = d.Dial(context.Background(), "my-project:my-region:my-instance", WithPrivateIP())
	if !mock.ErrorContains(err, "does not have IP of type") {
		t.Fatalf("expected Dial to fail with missing metadata")
	}

	// when Dialing TCP socket fails (no server proxy running)
	_, err = d.Dial(context.Background(), "my-project:my-region:my-instance")
	if !mock.ErrorContains(err, "connection refused") {
		t.Fatalf("expected Dial to fail with connection error")
	}

	inst.Cert.NotAfter = time.Now().Add(-time.Hour)
	stop := mock.StartServerProxy(t, inst)
	defer stop()

	// when TLS handshake fails
	_, err = d.Dial(context.Background(), "my-project:my-region:my-instance")
	if !mock.ErrorContains(err, "handshake failed") {
		t.Fatalf("expected Dial to fail with connection error")
	}
}
