package remotecoordinator

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Tutitoos/atenea/pkg/remoteprotocol"
)

func TestInvalidRFC6455FramesAreRejectedByGorillaBeforeApplicationClose(t *testing.T) {
	material := newTLSMaterial(t)
	auth := &recordingAuthenticator{}
	server, _, fingerprint := configuredHandler(t, material, auth)
	spkiDigest := sha256.Sum256(material.clientCert.RawSubjectPublicKeyInfo)
	marker := "frame-marker-never-echoed"

	tests := []struct {
		name  string
		frame []byte
	}{
		{
			name: "unmasked data",
			// The payload marker is deliberately present after an invalid
			// header. Gorilla rejects the MASK bit before reading it.
			frame: append([]byte{0x81, byte(len(marker))}, []byte(marker)...),
		},
		{
			name: "reserved opcode",
			// FIN + reserved opcode 0xb + client mask bit, zero payload.
			frame: []byte{0x8b, 0x80, 0x11, 0x22, 0x33, 0x44},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			conn, reader := rawTLSWebSocket(t, server, material, marker)
			if _, err := conn.Write(test.frame); err != nil {
				t.Fatal(err)
			}
			code, reason := readRawClose(t, reader)
			if code != websocket.CloseProtocolError {
				t.Fatalf("first close code = %d, want %d", code, websocket.CloseProtocolError)
			}
			if reason == "" || strings.Contains(reason, marker) || strings.Contains(reason, material.clientCert.Subject.CommonName) || strings.Contains(reason, fingerprint) || strings.Contains(reason, hex.EncodeToString(spkiDigest[:])) || strings.Contains(reason, "secret") {
				t.Fatalf("first close reason contains sensitive/peer data: %q", reason)
			}
			if calls, _ := auth.snapshot(); calls != 0 {
				t.Fatalf("authentication calls = %d, want zero", calls)
			}
			_ = conn.Close()
		})
	}
}

func rawTLSWebSocket(t *testing.T, server *httptest.Server, material testTLSMaterial, marker string) (net.Conn, *bufio.Reader) {
	t.Helper()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	tcp, err := net.DialTimeout("tcp", parsed.Host, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	conn := tls.Client(tcp, &tls.Config{RootCAs: material.caPool, Certificates: []tls.Certificate{material.clientTLS}, ServerName: "localhost", MinVersion: tls.VersionTLS13})
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.Handshake(); err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 16))
	host := parsed.Host
	request := fmt.Sprintf("GET %s?marker=%s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Protocol: %s\r\nX-Forwarded-For: %s\r\n\r\n", ConnectPath, url.QueryEscape(marker), host, key, remoteprotocol.Subprotocol, marker)
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols || response.Header.Get("Sec-WebSocket-Protocol") != remoteprotocol.Subprotocol {
		_ = response.Body.Close()
		t.Fatalf("handshake status=%d protocol=%q", response.StatusCode, response.Header.Get("Sec-WebSocket-Protocol"))
	}
	_ = response.Body.Close()
	return conn, reader
}

func readRawClose(t *testing.T, reader *bufio.Reader) (int, string) {
	t.Helper()
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil {
		t.Fatal(err)
	}
	if header[0]&0x0f != 0x08 {
		t.Fatalf("first server frame opcode = %d, want close", header[0]&0x0f)
	}
	payloadLength := int(header[1] & 0x7f)
	switch payloadLength {
	case 126:
		var extended [2]byte
		if _, err := io.ReadFull(reader, extended[:]); err != nil {
			t.Fatal(err)
		}
		payloadLength = int(binary.BigEndian.Uint16(extended[:]))
	case 127:
		t.Fatal("unexpected oversized close frame")
	}
	if header[1]&0x80 != 0 {
		t.Fatal("server close frame must not be masked")
	}
	payload := make([]byte, payloadLength)
	if _, err := io.ReadFull(reader, payload); err != nil {
		t.Fatal(err)
	}
	if len(payload) < 2 {
		t.Fatalf("close payload length = %d", len(payload))
	}
	return int(binary.BigEndian.Uint16(payload[:2])), string(payload[2:])
}
