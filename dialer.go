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
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"fmt"
	"net"
	"sync"
	"time"

	"cloud.google.com/cloudsqlconn/internal/cloudsql"
	"golang.org/x/net/proxy"
	"google.golang.org/api/option"
	sqladmin "google.golang.org/api/sqladmin/v1beta4"
)

const (
	// versionString indicates the version of this library.
	versionString = "0.1.0-dev"
	userAgent     = "cloud-sql-go-connector/" + versionString

	// defaultTCPKeepAlive is the default keep alive value used on connections to a Cloud SQL instance.
	defaultTCPKeepAlive = 30 * time.Second
	// serverProxyPort is the port the server-side proxy receives connections on.
	serverProxyPort = "3307"
)

var (
	// defaultKey is the default RSA public/private keypair used by the clients.
	defaultKey    *rsa.PrivateKey
	defaultKeyErr error
	keyOnce       sync.Once
)

func getDefaultKeys() (*rsa.PrivateKey, error) {
	keyOnce.Do(func() {
		defaultKey, defaultKeyErr = rsa.GenerateKey(rand.Reader, 2048)
	})
	return defaultKey, defaultKeyErr
}

// A Dialer is used to create connections to Cloud SQL instances.
//
// Use NewDialer to initialize a Dialer.
type Dialer struct {
	lock           sync.RWMutex
	instances      map[cloudsql.ConnName]*cloudsql.Instance
	key            *rsa.PrivateKey
	refreshTimeout time.Duration

	sqladmin *sqladmin.Service

	// defaultDialCfg holds the constructor level DialOptions, so that it can
	// be copied and mutated by the Dial function.
	defaultDialCfg dialCfg
}

// NewDialer creates a new Dialer.
//
// Initial calls to NewDialer make take longer than normal because generation of an
// RSA keypair is performed. Calls with a WithRSAKeyPair DialOption or after a default
// RSA keypair is generated will be faster.
func NewDialer(ctx context.Context, opts ...DialerOption) (*Dialer, error) {
	cfg := &dialerConfig{
		refreshTimeout: 30 * time.Second,
		sqladminOpts:   []option.ClientOption{option.WithUserAgent(userAgent)},
	}
	for _, opt := range opts {
		opt(cfg)
	}

	if cfg.rsaKey == nil {
		key, err := getDefaultKeys()
		if err != nil {
			return nil, fmt.Errorf("failed to generate RSA keys: %v", err)
		}
		cfg.rsaKey = key
	}

	client, err := sqladmin.NewService(ctx, cfg.sqladminOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create sqladmin client: %v", err)
	}

	dialCfg := dialCfg{
		ipType:       cloudsql.PublicIP,
		tcpKeepAlive: defaultTCPKeepAlive,
	}
	for _, opt := range cfg.dialOpts {
		opt(&dialCfg)
	}

	d := &Dialer{
		instances:      make(map[cloudsql.ConnName]*cloudsql.Instance),
		key:            cfg.rsaKey,
		refreshTimeout: cfg.refreshTimeout,
		sqladmin:       client,
		defaultDialCfg: dialCfg,
	}
	return d, nil
}

// Dial returns a net.Conn connected to the specified Cloud SQL instance. The instance argument must be the
// instance's connection name, which is in the format "project-name:region:instance-name".
func (d *Dialer) Dial(ctx context.Context, instance string, opts ...DialOption) (net.Conn, error) {
	cfg := d.defaultDialCfg
	for _, opt := range opts {
		opt(&cfg)
	}

	cn, err := cloudsql.NewConnName(instance)
	if err != nil {
		return nil, err
	}

	i, err := d.instance(cn)
	if err != nil {
		return nil, err
	}
	addr, tlsCfg, err := i.ConnectInfo(ctx, cfg.ipType)
	if err != nil {
		return nil, err
	}
	addr = net.JoinHostPort(addr, serverProxyPort)

	conn, err := proxy.Dial(ctx, "tcp", addr)
	if err != nil {
		// refresh the instance info in case it caused the connection failure
		i.ForceRefresh()
		return nil, err
	}
	if c, ok := conn.(*net.TCPConn); ok {
		if err := c.SetKeepAlive(true); err != nil {
			return nil, fmt.Errorf("failed to set keep-alive: %v", err)
		}
		if err := c.SetKeepAlivePeriod(cfg.tcpKeepAlive); err != nil {
			return nil, fmt.Errorf("failed to set keep-alive period: %v", err)
		}
	}
	tlsConn := tls.Client(conn, tlsCfg)
	if err := tlsConn.Handshake(); err != nil {
		// refresh the instance info in case it caused the handshake failure
		i.ForceRefresh()
		_ = tlsConn.Close() // best effort close attempt
		return nil, fmt.Errorf("handshake failed: %w", err)
	}
	return tlsConn, nil
}

func (d *Dialer) instance(cn cloudsql.ConnName) (*cloudsql.Instance, error) {
	// Check instance cache
	d.lock.RLock()
	i, ok := d.instances[cn]
	d.lock.RUnlock()
	if !ok {
		d.lock.Lock()
		// Recheck to ensure instance wasn't created between locks
		i, ok = d.instances[cn]
		if !ok {
			i = cloudsql.NewInstance(cn, d.sqladmin, d.key, d.refreshTimeout)
			d.instances[cn] = i
		}
		d.lock.Unlock()
	}
	return i, nil
}
