// Package transport moves encrypted membership packets over MTU-bounded UDP
// and encrypted full-state snapshots over length-bounded TCP.
//
// Both transports use the cluster keyring (AES-256-GCM). Packets that fail
// authentication are dropped before any parsing happens, so an attacker
// without the key cannot reach the membership state machine.
package transport

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/manziosee/atlas/internal/metrics"
	"github.com/manziosee/atlas/internal/security"
)

// MaxPayload is sized to fit a 1280-byte IPv6 MTU after IPv6, UDP and the
// versioned AES-GCM envelope headers (40 + 8 + 29 bytes).
const MaxPayload = 1200

// maxDatagram bounds the receive buffer (largest possible UDP payload).
const maxDatagram = 65507

// aad binds ciphertexts to this protocol so they cannot be replayed into a
// different Atlas subsystem that shares the key.
var aad = []byte("atlas/gossip/v1")

// ErrPayloadTooLarge is returned by Send for payloads over MaxPayload.
var ErrPayloadTooLarge = errors.New("transport: payload too large")

// Packet is an authenticated, decrypted datagram or TCP stream frame.
type Packet struct {
	From       *net.UDPAddr
	Payload    []byte
	ReceivedAt time.Time
	Stream     bool
}

// UDP is an encrypted UDP transport.
type UDP struct {
	conn        *net.UDPConn
	tcp         *net.TCPListener
	keyring     *security.Keyring
	log         *slog.Logger
	packets     chan Packet
	streamSlots chan struct{}

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

// MaxStreamPayload bounds a decrypted TCP full-state frame.
const MaxStreamPayload = 16 << 20

const maxStreamFrame = MaxStreamPayload + 64

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
	ta, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("transport: resolve TCP %q: %w", addr, err)
	}
	if ta.Port == 0 {
		udpAddr := conn.LocalAddr().(*net.UDPAddr)
		ta = &net.TCPAddr{IP: udpAddr.IP, Port: udpAddr.Port, Zone: udpAddr.Zone}
	}
	tcp, err := net.ListenTCP("tcp", ta)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("transport: listen TCP %q: %w", addr, err)
	}
	// Larger kernel buffers absorb bursts during gossip storms.
	_ = conn.SetReadBuffer(4 << 20)
	_ = conn.SetWriteBuffer(4 << 20)

	t := &UDP{
		conn:        conn,
		tcp:         tcp,
		keyring:     kr,
		log:         log.With("component", "transport"),
		packets:     make(chan Packet, 1024),
		streamSlots: make(chan struct{}, 64),
		resolved:    make(map[string]resolvedAddr),
		done:        make(chan struct{}),
	}
	go t.readLoop()
	go t.acceptStreamLoop()
	return t, nil
}

// SendStream sends one authenticated, length-prefixed message over TCP. It is
// used for bounded full-state push-pull, where UDP would require fragmentation.
func (t *UDP) SendStream(ctx context.Context, addr string, payload []byte) error {
	if len(payload) == 0 || len(payload) > MaxStreamPayload {
		return fmt.Errorf("transport: TCP payload length %d is outside limits", len(payload))
	}
	remote, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		return fmt.Errorf("transport: resolve TCP peer %q: %w", addr, err)
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", remote.String())
	if err != nil {
		return fmt.Errorf("transport: connect TCP peer %q: %w", addr, err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	sealed, err := t.keyring.Seal(payload, aad)
	if err != nil {
		return err
	}
	if len(sealed) > maxStreamFrame {
		return errors.New("transport: sealed TCP frame exceeds limit")
	}
	if uint64(len(sealed)) > uint64(^uint32(0)) {
		return errors.New("transport: sealed TCP frame length overflows its header")
	}
	frame := make([]byte, 4+len(sealed))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(sealed))) // #nosec G115 -- length is bounded by checks above.
	copy(frame[4:], sealed)
	if err := writeAll(conn, frame); err != nil {
		return fmt.Errorf("transport: send TCP frame to %s: %w", addr, err)
	}
	metrics.PacketsSent.Inc()
	metrics.BytesSent.Add(float64(len(frame)))
	return nil
}

func writeAll(writer io.Writer, payload []byte) error {
	for len(payload) > 0 {
		written, err := writer.Write(payload)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		payload = payload[written:]
	}
	return nil
}

func (t *UDP) acceptStreamLoop() {
	for {
		conn, err := t.tcp.AcceptTCP()
		if err != nil {
			select {
			case <-t.done:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			t.log.Warn("TCP accept failed", "err", err)
			continue
		}
		select {
		case t.streamSlots <- struct{}{}:
			go func() {
				defer func() { <-t.streamSlots }()
				t.handleStreamConn(conn)
			}()
		default:
			_ = conn.Close()
		}
	}
}

func (t *UDP) handleStreamConn(conn *net.TCPConn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	var prefix [4]byte
	if _, err := io.ReadFull(conn, prefix[:]); err != nil {
		return
	}
	length := binary.BigEndian.Uint32(prefix[:])
	if length == 0 || length > maxStreamFrame {
		return
	}
	sealed := make([]byte, int(length))
	if _, err := io.ReadFull(conn, sealed); err != nil {
		return
	}
	metrics.BytesReceived.Add(float64(length + 4))
	plaintext, err := t.keyring.Open(sealed, aad)
	if err != nil {
		metrics.PacketsRejected.WithLabelValues("decrypt").Inc()
		return
	}
	metrics.PacketsReceived.Inc()
	remote, ok := conn.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return
	}
	select {
	case t.packets <- Packet{From: &net.UDPAddr{IP: remote.IP, Port: remote.Port, Zone: remote.Zone}, Payload: plaintext, ReceivedAt: time.Now(), Stream: true}:
	default:
		metrics.PacketsRejected.WithLabelValues("backpressure").Inc()
	}
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
		if len(pt) > MaxPayload {
			metrics.PacketsRejected.WithLabelValues("oversize").Inc()
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
		if closeErr := t.tcp.Close(); err == nil {
			err = closeErr
		}
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
