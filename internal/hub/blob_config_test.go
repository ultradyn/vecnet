package hub

import (
	"context"
	"encoding/binary"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XertroV/vecnet/internal/auth"
	"github.com/XertroV/vecnet/internal/blobwire"
)

// Use production bootstrap (no R2 client injection) with fake DNS. The fallback
// default transport is trapped at a counting listener so even a regression can
// never reach the real provider. No tests in this process run in parallel with
// this test's temporary resolver/transport replacement.
func TestR2ProductionEgressRejectsPrivateCompletionAndDeletion(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.0.0.1"} {
		t.Run(ip, func(t *testing.T) {
			env := newTransferTestEnv(t)
			var lookups atomic.Int32
			resolver := r2PrivateResolver(t, net.ParseIP(ip), &lookups)
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal("counting listener failed")
			}
			defer listener.Close()
			accepted := make(chan string, 16)
			go func() {
				for {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					accepted <- conn.RemoteAddr().String()
					conn.Close()
				}
			}()
			oldResolver, oldTransport := net.DefaultResolver, http.DefaultTransport
			trap := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, listener.Addr().String())
			}}
			net.DefaultResolver, http.DefaultTransport = resolver, trap
			defer func() {
				net.DefaultResolver, http.DefaultTransport = oldResolver, oldTransport
				trap.CloseIdleConnections()
			}()
			h := env.Hub
			h.Cfg.BlobBackend = "r2"
			h.Cfg.AllowPrivateEgress = false
			h.Cfg.R2 = R2Config{AccountID: "00000000000000000000000000000000", Bucket: "test-bucket", ChecksumProfile: "sha256", StorageQuotaBytes: 100, AccessKeyID: "FAKEACCESS", SecretAccessKey: "fake-signing-secret"}
			if err := h.initR2(); err != nil {
				t.Fatal("production bootstrap failed")
			}
			caller := auth.Caller{Kind: "operator", PeerID: env.OperatorPeer, PrincipalID: env.OperatorPrincipal}
			grant, err := h.CreateBlobUpload(context.Background(), caller, "private-egress", blobwire.UploadRequest{Size: 3, SHA256: hexSHA([]byte("abc")), MediaType: "text/plain"})
			if err != nil {
				t.Fatal("reservation failed")
			}
			_, err = h.CompleteBlobUpload(context.Background(), caller, grant.BlobID)
			requireBlobCode(t, err, "blob_unavailable")
			if apiErr := err.(*APIError); apiErr.Status != 503 || apiErr.Message != "blob_unavailable" {
				t.Error("completion error was not sanitized")
			}
			if lookups.Load() == 0 {
				t.Error("completion bypassed guarded DNS resolution")
			}
			beforeDelete := lookups.Load()
			env.Now.Advance(6 * time.Minute)
			if err := h.Blobs.Transfers.Tick(context.Background()); err != nil {
				t.Fatal("cleanup tick failed")
			}
			if lookups.Load() <= beforeDelete {
				t.Error("deletion bypassed guarded DNS resolution")
			}
			var attempts int
			var reason string
			if err := h.DB.QueryRow(`SELECT attempts,last_code FROM blob_delete_jobs WHERE blob_id=?`, grant.BlobID).Scan(&attempts, &reason); err != nil || attempts != 1 || reason != "blob_unavailable" {
				t.Error("blocked deletion did not retain sanitized retry state")
			}
			// A barrier connection makes the count deterministic without sleeps.
			barrier, err := net.Dial("tcp", listener.Addr().String())
			if err != nil {
				t.Fatal("count barrier failed")
			}
			defer barrier.Close()
			for count := 0; ; count++ {
				select {
				case addr := <-accepted:
					if addr == barrier.LocalAddr().String() {
						if count != 0 {
							t.Errorf("private destination received %d connections", count)
						}
						return
					}
				case <-time.After(5 * time.Second):
					t.Fatal("count barrier timed out")
				}
			}
		})
	}
}

// A minimal local DNS responder returns just one private A record (no AAAA).
// The resolver's Dial hook routes every DNS request to it, never to system DNS.
func r2PrivateResolver(t *testing.T, ip net.IP, lookups *atomic.Int32) *net.Resolver {
	t.Helper()
	dns, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("fake DNS listener failed")
	}
	t.Cleanup(func() { dns.Close() })
	go func() {
		buffer := make([]byte, 512)
		for {
			n, addr, err := dns.ReadFrom(buffer)
			if err != nil {
				return
			}
			if n < 17 {
				continue
			}
			end := 12
			for end < n && buffer[end] != 0 {
				end += int(buffer[end]) + 1
			}
			end += 5 // terminating label, type and class
			if end > n {
				continue
			}
			lookups.Add(1)
			response := append([]byte(nil), buffer[:end]...)
			binary.BigEndian.PutUint16(response[2:4], 0x8180)
			clear(response[6:12])
			if binary.BigEndian.Uint16(response[end-4:end-2]) == 1 {
				binary.BigEndian.PutUint16(response[6:8], 1)
				response = append(response, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 0, 0, 4)
				response = append(response, ip.To4()...)
			}
			_, _ = dns.WriteTo(response, addr)
		}
	}()
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "udp", dns.LocalAddr().String())
	}}
}
