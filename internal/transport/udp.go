// Package transport moves encrypted gossip packets over UDP.
//
// Every datagram is sealed with the cluster keyring (AES-256-GCM). Packets
// that fail authentication are dropped before any parsing happens, so an
// attacker without the key cannot reach the membership state machine.
package transport

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/manziosee/atlas/internal/metrics"
	"github.com/manziosee/atlas/internal/security"
)

// MaxPayload is the largest plaintext payload accepted for sending.
const MaxPayload = 32 * 1024

// maxDatagram bounds the receive buffer (largest possible UDP payload).
const maxDatagram = 65507

// aad binds ciphertexts to this protocol so they cannot be replayed into a
// different Atlas subsystem that shares the key.
var aad = []byte("atlas/gossip/v1")

// ErrPayloadTooLarge is returned by Send for payloads over MaxPayload.
var ErrPayloadTooLarge = errors.New("transport: payload too large")

// Packet is a decrypted, authenticated datagram.
type Packet struct {
	From       *net.UDPAddr
	Payload    []byte
	ReceivedAt time.Time
}

// UDP is an encrypted UDP transport.
type UDP struct {
	conn    *net.UDPConn
	keyring *security.Keyring
	log     *slog.Logger
	packets chan Packet

	resolveMu sync.Mutex
	resolved  map[string]resolvedAddr

	closeOnce sync.Once
	done      chan struct{}
}

type resolvedAddr struct {
	addr    *net.UDPAddr
	expires time.Time
}

const resolveTTL = 30 * time.Second

// Listen binds a UDP socket on addr and starts the receive loop.
func Listen(addr string, kr *security.Keyring, log *slog.Logger) (*UDP, error) {
	ua, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("transport: resolve %q: %w", addr, err)
	}
	conn, err := net.ListenUDP("udp", ua)
	if err != nil {
		return nil, fmt.Errorf("transport: listen %q: %w", addr, err)
	}
	// Larger kernel buffers absorb bursts during gossip storms.
	_ = conn.SetReadBuffer(4 << 20)
	_ = conn.SetWriteBuffer(4 << 20)

	t := &UDP{
		conn:     conn,
		keyring:  kr,
		log:      log.With("component", "transport"),
		packets:  make(chan Packet, 1024),
		resolved: make(map[string]resolvedAddr),
		done:     make(chan struct{}),
	}
	go t.readLoop()
	return t, nil
}

// LocalAddr returns the bound address.
func (t *UDP) LocalAddr() *net.UDPAddr { return t.conn.LocalAddr().(*net.UDPAddr) }

// Packets delivers authenticated packets. It is closed when the transport closes.
func (t *UDP) Packets() <-chan Packet { return t.packets }

// Send encrypts payload and sends it to addr ("host:port"; hostnames are
// resolved and cached).
func (t *UDP) Send(addr string, payload []byte) error {
	if len(payload) > MaxPayload {
		return ErrPayloadTooLarge
	}
	ua, err := t.resolve(addr)
	if err != nil {
		return err
	}
	sealed, err := t.keyring.Seal(payload, aad)
	if err != nil {
		return err
	}
	n, err := t.conn.WriteToUDP(sealed, ua)
	if err != nil {
		return fmt.Errorf("transport: send to %s: %w", addr, err)
	}
	metrics.PacketsSent.Inc()
	metrics.BytesSent.Add(float64(n))
	return nil
}

func (t *UDP) resolve(addr string) (*net.UDPAddr, error) {
	t.resolveMu.Lock()
	defer t.resolveMu.Unlock()
	if r, ok := t.resolved[addr]; ok && time.Now().Before(r.expires) {
		return r.addr, nil
	}
	ua, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("transport: resolve %q: %w", addr, err)
	}
	t.resolved[addr] = resolvedAddr{addr: ua, expires: time.Now().Add(resolveTTL)}
	return ua, nil
}

func (t *UDP) readLoop() {
	defer close(t.packets)
	buf := make([]byte, maxDatagram)
	for {
		n, from, err := t.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-t.done:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			t.log.Warn("read failed", "err", err)
			continue
		}
		metrics.BytesReceived.Add(float64(n))

		pt, err := t.keyring.Open(buf[:n], aad)
		if err != nil {
			metrics.PacketsRejected.WithLabelValues("decrypt").Inc()
			t.log.Debug("dropped unauthenticated packet", "from", from)
			continue
		}
		metrics.PacketsReceived.Inc()

		select {
		case t.packets <- Packet{From: from, Payload: pt, ReceivedAt: time.Now()}:
		default:
			metrics.PacketsRejected.WithLabelValues("backpressure").Inc()
		}
	}
}

// Close stops the receive loop and releases the socket.
func (t *UDP) Close() error {
	var err error
	t.closeOnce.Do(func() {
		close(t.done)
		err = t.conn.Close()
	})
	return err
}

// AdvertiseAddr picks the address peers should use to reach this node when
// none is configured: the bind address if it is specific, otherwise the
// first private non-loopback interface address.
func AdvertiseAddr(bind string) (string, error) {
	host, port, err := net.SplitHostPort(bind)
	if err != nil {
		return "", err
	}
	if ip := net.ParseIP(host); ip != nil && !ip.IsUnspecified() {
		return bind, nil
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}
	var fallback net.IP
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.IsLoopback() || ipn.IP.IsLinkLocalUnicast() {
			continue
		}
		if ipn.IP.IsPrivate() {
			return net.JoinHostPort(ipn.IP.String(), port), nil
		}
		if fallback == nil {
			fallback = ipn.IP
		}
	}
	if fallback != nil {
		return net.JoinHostPort(fallback.String(), port), nil
	}
	return "", errors.New("transport: no usable interface address; set ATLAS_GOSSIP_ADVERTISE_ADDR")
}
