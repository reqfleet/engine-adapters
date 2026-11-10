//go:build integration && linux

// Adapted from engine-sdk v0.3.0 examples/preparing-adapter/conformance_test.go.
// Copyright reqfleet contributors. Apache-2.0; modified for real adapter processes.
package conformance

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"sync"
	"testing"
	"time"

	"github.com/reqfleet/engine-sdk/protocol"
	"github.com/stretchr/testify/require"
)

const (
	proofCollection = "proof-collection"
	proofPlan       = "proof-plan"
	proofKey        = "proof-secret"
	proofWait       = 20 * time.Second
)

func proofPayload(verb, runID, filename string, settings json.RawMessage) protocol.Payload {
	message := &protocol.EngineMessage{Verb: verb, RunID: runID, TestFile: filename}
	if verb == protocol.VerbStart {
		message.RunConfigs = map[string]protocol.RunSpec{"7": {Settings: settings}}
	} else if verb == protocol.VerbStop {
		message.StopReason = protocol.StopCanceled
	}
	return protocol.Payload{Verb: verb, PlanMessage: protocol.PlanMessage{proofPlan: message}}
}

func proofProgress(client *http.Client, runID string) (protocol.RunStatus, error) {
	request, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:8080/progress?run_id="+runID, nil)
	if err != nil {
		return protocol.RunStatus{}, err
	}
	request.Header.Set("Authorization", "Bearer "+proofKey)
	response, err := client.Do(request)
	if err != nil {
		return protocol.RunStatus{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return protocol.RunStatus{}, fmt.Errorf("progress returned HTTP %d", response.StatusCode)
	}
	var status protocol.RunStatus
	err = json.NewDecoder(response.Body).Decode(&status)
	return status, err
}

func proofArtifactPath(t *testing.T, filename string) string {
	t.Helper()
	relative, err := protocol.TestFilePath(proofCollection, proofPlan, filename)
	require.NoError(t, err)
	return path.Join(protocol.ArtifactPathPrefix, relative)
}

func awaitProofEvent(t *testing.T, event <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-event:
	case <-time.After(proofWait):
		t.Fatalf("timed out waiting for %s", description)
	}
}

type proofLog struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (p *proofLog) Write(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.buffer.Write(data)
}

func (p *proofLog) String() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.buffer.String()
}

type proofPeer struct {
	mu         sync.Mutex
	connection net.Conn
	subscribed chan error
	done       chan struct{}
}

func newProofPeer(t *testing.T, certificate tls.Certificate) *proofPeer {
	t.Helper()
	listener, err := tls.Listen("tcp", "127.0.0.1:2416", &tls.Config{
		MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate},
	})
	require.NoError(t, err)
	peer := &proofPeer{subscribed: make(chan error, 1), done: make(chan struct{})}
	go func() {
		defer close(peer.done)
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			peer.subscribed <- acceptErr
			return
		}
		peer.mu.Lock()
		peer.connection = connection
		peer.mu.Unlock()
		_ = connection.SetReadDeadline(time.Now().Add(proofWait))
		var topic protocol.Topic
		decodeErr := json.NewDecoder(connection).Decode(&topic)
		if decodeErr == nil && (topic.Name != "collection:"+proofCollection || topic.Password != proofKey) {
			decodeErr = fmt.Errorf("unexpected subscription topic or API key: %+v", topic)
		}
		_ = connection.SetReadDeadline(time.Time{})
		peer.subscribed <- decodeErr
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		peer.mu.Lock()
		if peer.connection != nil {
			_ = peer.connection.Close()
		}
		peer.mu.Unlock()
		<-peer.done
		// Accept may have succeeded just before listener.Close but published
		// its connection after the first lock acquisition.
		peer.mu.Lock()
		if peer.connection != nil {
			_ = peer.connection.Close()
		}
		peer.mu.Unlock()
	})
	return peer
}

func (p *proofPeer) awaitSubscription(t *testing.T) {
	t.Helper()
	select {
	case err := <-p.subscribed:
		require.NoError(t, err)
	case <-time.After(proofWait):
		t.Fatal("adapter did not subscribe to the TLS command peer")
	}
}

func (p *proofPeer) send(payload protocol.Payload) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.connection == nil {
		return fmt.Errorf("adapter has not connected to command peer")
	}
	if err := p.connection.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		return err
	}
	return json.NewEncoder(p.connection).Encode(payload)
}

func startProofHTTP(t *testing.T, address string, handler http.Handler, certificate *tls.Certificate) {
	t.Helper()
	listener, err := net.Listen("tcp", address)
	require.NoError(t, err)
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: handler}}
	if certificate == nil {
		server.Start()
	} else {
		server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{*certificate}}
		server.StartTLS()
	}
	t.Cleanup(func() {
		server.CloseClientConnections()
		server.Close()
	})
}

func startProofDNS(t *testing.T) {
	t.Helper()
	listener, err := net.ListenPacket("udp", "127.0.0.1:53")
	require.NoError(t, err)
	done := make(chan struct{})
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
	})
	go func() {
		defer close(done)
		buffer := make([]byte, 4096)
		for {
			n, address, err := listener.ReadFrom(buffer)
			if err != nil {
				return
			}
			if n < 12 {
				continue
			}
			// NXDOMAIN, retaining the question and any EDNS record. No
			// external resolver or network is involved in DNS failure tests.
			buffer[2] |= 0x80
			buffer[3] = 0x83
			if _, err := listener.WriteTo(buffer[:n], address); err != nil {
				return
			}
		}
	}()
	require.NoError(t, os.WriteFile("/etc/resolv.conf", []byte("nameserver 127.0.0.1\noptions attempts:1 timeout:1\n"), 0644))
}

func proofCertificate(t *testing.T) (tls.Certificate, string) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ca := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Public adapter proof CA"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, caKey)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	require.NoError(t, err)
	certificate, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	)
	require.NoError(t, err)
	return certificate, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
}
