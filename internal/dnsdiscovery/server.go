// Package dnsdiscovery serves read-only DNS views of the Atlas service registry.
package dnsdiscovery

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/manziosee/atlas/internal/metrics"
	"github.com/manziosee/atlas/internal/storage"
)

const (
	maxTCP       = 64
	maxUDPSize   = 4096
	maxInstances = 8
)

// Registry is the read interface needed by the DNS server.
type Registry interface {
	List(context.Context, storage.Filter) ([]storage.Service, error)
	Get(context.Context, string) (storage.Service, error)
}

// Server translates names under atlas. into service records. A and AAAA
// queries return healthy IP instances; SRV queries return healthy instances
// and their registered ports. A service name may include a region label.
type Server struct {
	registry Registry
	zone     string
	ttl      uint32
	next     atomic.Uint64
}

func New(registry Registry, zone string, ttl time.Duration) (*Server, error) {
	if registry == nil {
		return nil, errors.New("dns discovery: registry is required")
	}
	zone = strings.Trim(strings.TrimSpace(zone), ".")
	if zone == "" {
		zone = "atlas"
	}
	if ttl < time.Second || ttl > time.Hour {
		return nil, errors.New("dns discovery: TTL must be between 1s and 1h")
	}
	if _, err := dnsmessage.NewName(zone + "."); err != nil {
		return nil, fmt.Errorf("dns discovery: invalid zone: %w", err)
	}
	for _, label := range strings.Split(zone, ".") {
		if !validLabel(label) {
			return nil, fmt.Errorf("dns discovery: invalid zone label %q", label)
		}
	}
	return &Server{registry: registry, zone: strings.ToLower(zone), ttl: uint32(ttl.Seconds())}, nil
}

// Serve runs UDP and TCP DNS on addr. The caller should use an explicit,
// private-network bind address because DNS intentionally has no auth layer.
func (s *Server) Serve(ctx context.Context, addr string) error {
	listenConfig := net.ListenConfig{}
	udp, err := listenConfig.ListenPacket(ctx, "udp", addr)
	if err != nil {
		return fmt.Errorf("dns discovery: listen UDP: %w", err)
	}
	tcp, err := listenConfig.Listen(ctx, "tcp", addr)
	if err != nil {
		_ = udp.Close()
		return fmt.Errorf("dns discovery: listen TCP: %w", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	wg.Go(func() { errCh <- s.serveUDP(ctx, udp) })
	wg.Go(func() { errCh <- s.serveTCP(ctx, tcp) })
	select {
	case <-ctx.Done():
	case err := <-errCh:
		if err != nil && !errors.Is(err, net.ErrClosed) {
			cancel()
			_ = udp.Close()
			_ = tcp.Close()
			wg.Wait()
			return err
		}
	}
	_ = udp.Close()
	_ = tcp.Close()
	wg.Wait()
	return nil
}

func (s *Server) serveUDP(ctx context.Context, conn net.PacketConn) error {
	buf := make([]byte, maxUDPSize)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return err
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return fmt.Errorf("dns discovery: read UDP: %w", err)
		}
		response := s.answer(ctx, buf[:n])
		if len(response) > 0 {
			if _, err := conn.WriteTo(response, addr); err != nil && ctx.Err() == nil {
				return fmt.Errorf("dns discovery: write UDP: %w", err)
			}
		}
	}
}

func (s *Server) serveTCP(ctx context.Context, listener net.Listener) error {
	semaphore := make(chan struct{}, maxTCP)
	var workers sync.WaitGroup
	defer workers.Wait()
	for {
		_ = listener.(*net.TCPListener).SetDeadline(time.Now().Add(time.Second))
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return err
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return fmt.Errorf("dns discovery: accept TCP: %w", err)
		}
		select {
		case semaphore <- struct{}{}:
			workers.Go(func() {
				defer func() { <-semaphore; _ = conn.Close() }()
				s.handleTCP(ctx, conn)
			})
		case <-ctx.Done():
			_ = conn.Close()
			return nil
		default:
			_ = conn.Close()
		}
	}
}

func (s *Server) handleTCP(ctx context.Context, conn net.Conn) {
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	for {
		var prefix [2]byte
		if _, err := io.ReadFull(conn, prefix[:]); err != nil {
			return
		}
		length := int(binary.BigEndian.Uint16(prefix[:]))
		if length < 12 {
			return
		}
		query := make([]byte, length)
		if _, err := io.ReadFull(conn, query); err != nil {
			return
		}
		response := s.answer(ctx, query)
		if len(response) == 0 || len(response) > 65535 {
			return
		}
		// The DNS TCP payload length was checked above to fit in 16 bits.
		binary.BigEndian.PutUint16(prefix[:], uint16(len(response))) //nolint:gosec // guarded by the 65535-byte check
		if writeAll(conn, prefix[:]) != nil || writeAll(conn, response) != nil {
			return
		}
	}
}

func writeAll(w io.Writer, packet []byte) error {
	for len(packet) > 0 {
		n, err := w.Write(packet)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		packet = packet[n:]
	}
	return nil
}

func (s *Server) answer(ctx context.Context, packet []byte) []byte {
	var req dnsmessage.Message
	if err := req.Unpack(packet); err != nil {
		metrics.DNSMalformedQueries.Inc()
		return nil
	}
	response := dnsmessage.Message{Header: dnsmessage.Header{
		ID: req.ID, Response: true, Authoritative: true,
		RecursionDesired: req.RecursionDesired,
	}}
	if len(req.Questions) != 1 || req.OpCode != 0 || req.Response {
		response.RCode = dnsmessage.RCodeFormatError
		return pack(response)
	}
	q := req.Questions[0]
	response.Questions = req.Questions
	if q.Class != dnsmessage.ClassINET {
		response.RCode = dnsmessage.RCodeRefused
		return pack(response)
	}
	if q.Type != dnsmessage.TypeA && q.Type != dnsmessage.TypeAAAA && q.Type != dnsmessage.TypeSRV {
		response.RCode = dnsmessage.RCodeNotImplemented
		return pack(response)
	}
	if id, ok := s.parseInstanceName(q.Name.String()); ok {
		service, err := s.registry.Get(ctx, id)
		if err != nil || !service.Routable() {
			response.RCode = dnsmessage.RCodeNameError
			return pack(response)
		}
		host, _, err := net.SplitHostPort(service.Address)
		if err == nil {
			ip := net.ParseIP(host)
			if q.Type == dnsmessage.TypeA && ip.To4() != nil {
				var address [4]byte
				copy(address[:], ip.To4())
				response.Answers = append(response.Answers, dnsmessage.Resource{Header: s.header(q.Name, dnsmessage.TypeA), Body: &dnsmessage.AResource{A: address}})
			} else if q.Type == dnsmessage.TypeAAAA && ip.To16() != nil && ip.To4() == nil {
				var address [16]byte
				copy(address[:], ip.To16())
				response.Answers = append(response.Answers, dnsmessage.Resource{Header: s.header(q.Name, dnsmessage.TypeAAAA), Body: &dnsmessage.AAAAResource{AAAA: address}})
			}
		}
		return pack(response)
	}
	name, region, ok := s.parseName(q.Name.String())
	if !ok {
		response.RCode = dnsmessage.RCodeNameError
		return pack(response)
	}
	services, err := s.registry.List(ctx, storage.Filter{Name: name, Region: region})
	if err != nil {
		response.RCode = dnsmessage.RCodeServerFailure
		return pack(response)
	}
	filtered := make([]storage.Service, 0, len(services))
	for _, service := range services {
		if service.Health == storage.HealthHealthy && service.Routable() {
			filtered = append(filtered, service)
		}
	}
	if len(filtered) == 0 {
		for _, service := range services {
			if service.Health == storage.HealthUnknown && service.Routable() {
				filtered = append(filtered, service)
			}
		}
	}
	if len(filtered) == 0 {
		response.RCode = dnsmessage.RCodeNameError
		return pack(response)
	}
	if len(filtered) > maxInstances {
		start := int((s.next.Add(1) - 1) % uint64(len(filtered))) //nolint:gosec // modulo bounds the result to len(filtered)
		rotated := make([]storage.Service, 0, maxInstances)
		for i := 0; i < maxInstances; i++ {
			rotated = append(rotated, filtered[(start+i)%len(filtered)])
		}
		filtered = rotated
	}
	for _, service := range filtered {
		host, portText, err := net.SplitHostPort(service.Address)
		if err != nil {
			continue
		}
		port, err := strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 {
			continue
		}
		ip := net.ParseIP(host)
		switch q.Type {
		case dnsmessage.TypeA:
			if v4 := ip.To4(); v4 != nil {
				var address [4]byte
				copy(address[:], v4)
				response.Answers = append(response.Answers, dnsmessage.Resource{Header: s.header(q.Name, dnsmessage.TypeA), Body: &dnsmessage.AResource{A: address}})
			}
		case dnsmessage.TypeAAAA:
			if v6 := ip.To16(); v6 != nil && ip.To4() == nil {
				var address [16]byte
				copy(address[:], v6)
				response.Answers = append(response.Answers, dnsmessage.Resource{Header: s.header(q.Name, dnsmessage.TypeAAAA), Body: &dnsmessage.AAAAResource{AAAA: address}})
			}
		case dnsmessage.TypeSRV:
			targetText := host
			if ip != nil {
				if !validLabel(service.ID) {
					continue
				}
				targetText = service.ID + ".instances." + s.zone + "."
			} else {
				if !validHostname(targetText) {
					continue
				}
				targetText = dnsFQDN(targetText)
			}
			target, err := dnsmessage.NewName(targetText)
			if err != nil {
				continue
			}
			response.Answers = append(response.Answers, dnsmessage.Resource{Header: s.header(q.Name, dnsmessage.TypeSRV), Body: &dnsmessage.SRVResource{Port: uint16(port), Target: target}}) //nolint:gosec // port is checked to be in [1, 65535]
			if ip != nil {
				if v4 := ip.To4(); v4 != nil {
					var address [4]byte
					copy(address[:], v4)
					response.Additionals = append(response.Additionals, dnsmessage.Resource{Header: s.header(target, dnsmessage.TypeA), Body: &dnsmessage.AResource{A: address}})
				} else if v6 := ip.To16(); v6 != nil {
					var address [16]byte
					copy(address[:], v6)
					response.Additionals = append(response.Additionals, dnsmessage.Resource{Header: s.header(target, dnsmessage.TypeAAAA), Body: &dnsmessage.AAAAResource{AAAA: address}})
				}
			}
		}
	}
	return pack(response)
}

func pack(msg dnsmessage.Message) []byte {
	queryType := "invalid"
	if len(msg.Questions) == 1 {
		switch msg.Questions[0].Type {
		case dnsmessage.TypeA:
			queryType = "A"
		case dnsmessage.TypeAAAA:
			queryType = "AAAA"
		case dnsmessage.TypeSRV:
			queryType = "SRV"
		default:
			queryType = "other"
		}
	}
	metrics.DNSQueries.WithLabelValues(queryType, strconv.Itoa(int(msg.RCode))).Inc()
	packet, err := msg.Pack()
	if err != nil {
		return nil
	}
	return packet
}

func (s *Server) header(name dnsmessage.Name, typ dnsmessage.Type) dnsmessage.ResourceHeader {
	return dnsmessage.ResourceHeader{Name: name, Type: typ, Class: dnsmessage.ClassINET, TTL: s.ttl}
}

func (s *Server) parseName(fqdn string) (name, region string, ok bool) {
	labels := strings.Split(strings.TrimSuffix(strings.ToLower(fqdn), "."), ".")
	zone := strings.Split(s.zone, ".")
	if len(labels) < len(zone)+1 || strings.Join(labels[len(labels)-len(zone):], ".") != strings.Join(zone, ".") {
		return "", "", false
	}
	prefix := labels[:len(labels)-len(zone)]
	if len(prefix) == 1 {
		return prefix[0], "", validServiceLabel(prefix[0])
	}
	if len(prefix) == 2 && strings.HasPrefix(prefix[0], "_") && strings.EqualFold(prefix[1], "_tcp") {
		name = strings.TrimPrefix(prefix[0], "_")
		return name, "", validServiceLabel(name)
	}
	if len(prefix) == 2 {
		return prefix[0], prefix[1], validServiceLabel(prefix[0]) && validLabel(prefix[1])
	}
	if len(prefix) == 3 && strings.HasPrefix(prefix[0], "_") && strings.EqualFold(prefix[1], "_tcp") {
		name = strings.TrimPrefix(prefix[0], "_")
		return name, prefix[2], validServiceLabel(name) && validLabel(prefix[2])
	}
	return "", "", false
}

func (s *Server) parseInstanceName(fqdn string) (string, bool) {
	labels := strings.Split(strings.TrimSuffix(strings.ToLower(fqdn), "."), ".")
	zone := strings.Split(s.zone, ".")
	if len(labels) != len(zone)+2 || labels[1] != "instances" || !validLabel(labels[0]) {
		return "", false
	}
	if strings.Join(labels[2:], ".") != strings.Join(zone, ".") {
		return "", false
	}
	return labels[0], true
}

func validLabel(label string) bool {
	if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	for _, r := range label {
		if !isAlnum(r) && r != '-' {
			return false
		}
	}
	return true
}

func validServiceLabel(label string) bool {
	if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	for _, r := range label {
		if !isAlnum(r) && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func isAlnum(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}

func validHostname(host string) bool {
	labels := strings.Split(strings.TrimSuffix(host, "."), ".")
	if len(labels) == 0 {
		return false
	}
	for _, label := range labels {
		if !validLabel(label) {
			return false
		}
	}
	return true
}

func dnsFQDN(host string) string {
	return strings.TrimSuffix(host, ".") + "."
}
