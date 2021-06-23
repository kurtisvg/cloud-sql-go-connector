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
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io/ioutil"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	sqladmin "google.golang.org/api/sqladmin/v1beta4"
)

// HTTPClient returns an *http.Client, URL, and cleanup function. The http.Client is
// configured to connect to test SSL Server at the returned URL. This server will
// respond to HTTP requests defined, or return a 5xx server error for unexpected ones.
// The cleanup function will close the server, and return an error if any expected calls
// weren't received.
func HTTPClient(requests ...*Request) (*http.Client, string, func() error) {
	// Create a TLS Server that responses to the requests defined
	s := httptest.NewTLSServer(http.HandlerFunc(
		func(resp http.ResponseWriter, req *http.Request) {
			for _, r := range requests {
				if r.matches(req) {
					r.handle(resp, req)
					return
				}
			}
			// Unexpected requests should throw an error
			resp.WriteHeader(http.StatusNotImplemented)
			// TODO: follow error format better?
			resp.Write([]byte(fmt.Sprintf("unexpected request sent to mock client: %v", req)))
		},
	))
	// cleanup stops the test server and checks for uncalled requests
	cleanup := func() error {
		s.Close()
		for i, e := range requests {
			if e.reqCt > 0 {
				return fmt.Errorf("%d calls left for specified call in pos %d: %v", e.reqCt, i, e)
			}
		}
		return nil
	}

	return s.Client(), s.URL, cleanup

}

// Request represents a HTTP request for a test Server to mock responses for.
//
// Use NewRequest to initialize new Requests.
type Request struct {
	sync.Mutex

	reqMethod string
	reqPath   string
	reqCt     int

	handle func(resp http.ResponseWriter, req *http.Request)
}

// matches returns true if a given http.Request should be handled by this Request.
func (r *Request) matches(hR *http.Request) bool {
	r.Lock()
	defer r.Unlock()
	if r.reqMethod != "" && r.reqMethod != hR.Method {
		return false
	}
	if r.reqPath != "" && r.reqPath != hR.URL.Path {
		return false
	}
	if r.reqCt <= 0 {
		return false
	}
	r.reqCt--
	return true
}

// IPMapping associates IP addresses with address types (public or private).
type IPMapping struct {
	// IPAddr is the IP address of a database instance.
	IPAddr string
	// Type is the IP address type (PRIMARY or PRIVATE)
	Type string
}

// InstanceGetSuccessWithOpts is like InstanceGetSuccess but allows a caller to
// configure the SQL Admin API response with specific database instance IP
// addresses.
func InstanceGetSuccessWithOpts(i FakeCSQLInstance, ct int, addrs []IPMapping) *Request {
	// Turn instance keys/certs into PEM encoded versions needed for response
	certBytes, err := x509.CreateCertificate(
		rand.Reader, i.Cert, i.Cert, &i.Key.PublicKey, i.Key)
	if err != nil {
		panic(err)
	}
	certPEM := new(bytes.Buffer)
	pem.Encode(certPEM, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certBytes,
	})

	var ipMap []*sqladmin.IpMapping
	for _, i := range addrs {
		ipMap = append(ipMap, &sqladmin.IpMapping{
			IpAddress: i.IPAddr,
			Type:      i.Type,
		})
	}

	db := &sqladmin.DatabaseInstance{
		BackendType:     "SECOND_GEN",
		ConnectionName:  fmt.Sprintf("%s:%s:%s", i.project, i.region, i.name),
		DatabaseVersion: i.dbVersion,
		Project:         i.project,
		Region:          i.region,
		Name:            i.name,
		IpAddresses:     ipMap,
		ServerCaCert:    &sqladmin.SslCert{Cert: certPEM.String()},
	}

	r := &Request{
		reqMethod: http.MethodGet,
		reqPath:   fmt.Sprintf("/sql/v1beta4/projects/%s/instances/%s", i.project, i.name),
		reqCt:     ct,
		handle: func(resp http.ResponseWriter, req *http.Request) {
			b, err := db.MarshalJSON()
			if err != nil {
				http.Error(resp, err.Error(), http.StatusInternalServerError)
				return
			}
			resp.WriteHeader(http.StatusOK)
			resp.Write(b)
		},
	}
	return r
}

// InstanceGetSuccess returns a Request that responds to the `instance.get` SQL Admin
// endpoint. It responds with a "StatusOK" and a DatabaseInstance object.
//
// https://cloud.google.com/sql/docs/mysql/admin-api/rest/v1beta4/instances/get
//
// To Configure the response from the SQL Admin endpoint, use
// InstanceGetSuccessWithOpts.
func InstanceGetSuccess(i FakeCSQLInstance, ct int) *Request {
	return InstanceGetSuccessWithOpts(i, ct, []IPMapping{{IPAddr: "127.0.0.1", Type: "PRIMARY"}})
}

// CreateEphemeralSuccess returns a Request that responds to the
// `sslCerts.createEphemeral` SQL Admin endpoint. It responds with a "StatusOK" and a
// SslCerts object.
//
// https://cloud.google.com/sql/docs/mysql/admin-api/rest/v1beta4/sslCerts/createEphemeral
func CreateEphemeralSuccess(i FakeCSQLInstance, ct int) *Request {
	r := &Request{
		reqMethod: http.MethodPost,
		reqPath:   fmt.Sprintf("/sql/v1beta4/projects/%s/instances/%s/createEphemeral", i.project, i.name),
		reqCt:     ct,
		handle: func(resp http.ResponseWriter, req *http.Request) {
			// Read the body from the request.
			b, err := ioutil.ReadAll(req.Body)
			defer req.Body.Close()
			if err != nil {
				http.Error(resp, fmt.Errorf("unable to read body: %w", err).Error(), http.StatusBadRequest)
				return
			}
			var eR sqladmin.SslCertsCreateEphemeralRequest
			err = json.Unmarshal(b, &eR)
			if err != nil {
				http.Error(resp, fmt.Errorf("invalid or unexpected json: %w", err).Error(), http.StatusBadRequest)
				return
			}
			// Extract the certificate from the request.
			bl, _ := pem.Decode([]byte(eR.PublicKey))
			if bl == nil {
				http.Error(resp, fmt.Errorf("unable to decode PublicKey: %w", err).Error(), http.StatusBadRequest)
				return
			}
			pubKey, err := x509.ParsePKIXPublicKey(bl.Bytes)
			if err != nil {
				http.Error(resp, fmt.Errorf("unable to decode PublicKey: %w", err).Error(), http.StatusBadRequest)
				return
			}

			// Create a signed cert from the client's public key.
			cert := &x509.Certificate{ // TODO: Validate this format vs API
				SerialNumber: &big.Int{},
				Subject: pkix.Name{
					Country:      []string{"US"},
					Organization: []string{"Google, Inc"},
					CommonName:   "Google Cloud SQL Client",
				},
				NotBefore:             time.Now(),
				NotAfter:              time.Now().Add(time.Hour), // 1 hour
				ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
				KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
				BasicConstraintsValid: true,
			}
			certBytes, err := x509.CreateCertificate(rand.Reader, cert, i.Cert, pubKey, i.Key)
			if err != nil {
				http.Error(resp, fmt.Errorf("unable to decode PublicKey: %w", err).Error(), http.StatusInternalServerError)
				return
			}
			certPEM := new(bytes.Buffer)
			pem.Encode(certPEM, &pem.Block{
				Type:  "CERTIFICATE",
				Bytes: certBytes,
			})

			// Return the signed cert to the client.
			c := sqladmin.SslCert{
				Cert:             certPEM.String(),
				CertSerialNumber: cert.SerialNumber.String(),
				CommonName:       cert.Subject.CommonName,
				CreateTime:       cert.NotBefore.Format(time.RFC3339),
				ExpirationTime:   cert.NotAfter.Format(time.RFC3339),
				Instance:         i.name,
			}
			b, err = c.MarshalJSON()
			if err != nil {
				http.Error(resp, fmt.Errorf("unable to encode response: %w", err).Error(), http.StatusInternalServerError)
				return
			}
			resp.WriteHeader(http.StatusOK)
			resp.Write(b)
		},
	}
	return r
}
