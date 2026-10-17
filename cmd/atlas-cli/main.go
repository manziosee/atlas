// Command atlas-cli inspects and operates an Atlas cluster.
//
// Connection settings come from flags or the environment:
//
//	ATLAS_ADDR         node API base URL   (default http://127.0.0.1:8080)
//	ATLAS_TOKEN        API bearer token
//	ATLAS_CA_FILE      CA bundle for HTTPS
//	ATLAS_CLIENT_CERT  client certificate for mTLS
//	ATLAS_CLIENT_KEY   client key for mTLS
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"text/tabwriter"
	"time"

	"github.com/manziosee/atlas/internal/membership"
	"github.com/manziosee/atlas/internal/security"
	"github.com/manziosee/atlas/internal/version"
)

const usage = `atlas-cli — operate an Atlas cluster

Usage:
  atlas-cli <command> [flags]

Commands:
  members    list the cluster as seen by a node
  node       show the node's own record
  health     exit 0 if the node is healthy (used by container health checks)
  keygen     generate a 256-bit gossip encryption key (base64)
  tokengen   generate a random API bearer token
  version    print version

Run 'atlas-cli <command> -h' for command flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]

	var err error
	switch cmd {
	case "members":
		err = members(args)
	case "node":
		err = node(args)
	case "health":
		err = health(args)
	case "keygen":
		err = printRandom(32, base64.StdEncoding)
	case "tokengen":
		err = printRandom(36, base64.RawURLEncoding)
	case "version":
		fmt.Println(version.String())
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func printRandom(n int, enc interface{ EncodeToString([]byte) string }) error {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	fmt.Println(enc.EncodeToString(b))
	return nil
}

type client struct {
	addr  string
	token string
	http  *http.Client
}

func newClient(name string, args []string) (*client, error) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	addr := fs.String("addr", envOr("ATLAS_ADDR", "http://127.0.0.1:8080"), "node API base URL")
	token := fs.String("token", os.Getenv("ATLAS_TOKEN"), "API bearer token (prefer ATLAS_TOKEN to keep it out of shell history)")
	ca := fs.String("ca", os.Getenv("ATLAS_CA_FILE"), "CA bundle for HTTPS")
	cert := fs.String("cert", os.Getenv("ATLAS_CLIENT_CERT"), "client certificate (mTLS)")
	key := fs.String("key", os.Getenv("ATLAS_CLIENT_KEY"), "client key (mTLS)")
	timeout := fs.Duration("timeout", 5*time.Second, "request timeout")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	tlsCfg, err := security.ClientTLSConfig(*ca, *cert, *key)
	if err != nil {
		return nil, err
	}
	return &client{
		addr:  *addr,
		token: *token,
		http: &http.Client{
			Timeout:   *timeout,
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		},
	}, nil
}

func (c *client) get(path string, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.http.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.addr+path, nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s %s", path, resp.Status, body)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

func members(args []string) error {
	c, err := newClient("members", args)
	if err != nil {
		return err
	}
	var resp struct {
		Members []membership.Member `json:"members"`
	}
	if err := c.get("/v1/members", &resp); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tADDRESS\tREGION\tSTATE\tINCARNATION\tSINCE")
	for _, m := range resp.Members {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\n", m.ID, m.Addr, m.Region, m.State, m.Incarnation, time.Since(m.Since).Round(time.Second))
	}
	return tw.Flush()
}

func node(args []string) error {
	c, err := newClient("node", args)
	if err != nil {
		return err
	}
	var resp map[string]any
	if err := c.get("/v1/node", &resp); err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(resp)
}

func health(args []string) error {
	c, err := newClient("health", args)
	if err != nil {
		return err
	}
	if err := c.get("/healthz", nil); err != nil {
		return errors.Join(errors.New("unhealthy"), err)
	}
	fmt.Println("ok")
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
