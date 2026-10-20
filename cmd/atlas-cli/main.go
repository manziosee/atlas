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
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/manziosee/atlas/internal/membership"
	"github.com/manziosee/atlas/internal/security"
	"github.com/manziosee/atlas/internal/storage"
	"github.com/manziosee/atlas/internal/version"
)

const usage = `atlas-cli — operate an Atlas cluster

Usage:
  atlas-cli <command> [flags]

Commands:
  members    list the cluster as seen by a node
  services   list registered service instances
  lookup     find instances by service name
  watch      stream membership events
  db-backup  create a consistent SQLite database snapshot
  db-restore restore a verified snapshot to a new database path
  completion print shell completion (bash|zsh|fish|powershell)
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
	case "services":
		err = services(args, false)
	case "lookup":
		err = services(args, true)
	case "watch":
		err = watch(args)
	case "db-backup":
		err = dbBackup(args)
	case "db-restore":
		err = dbRestore(args)
	case "completion":
		err = completion(args)
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

type cliConfigFile struct {
	Addr       string `json:"addr"`
	Token      string `json:"token"`
	CAFile     string `json:"ca_file"`
	ClientCert string `json:"client_cert"`
	ClientKey  string `json:"client_key"`
}

func newClient(name string, args []string) (*client, error) {
	return newClientWithTimeout(name, args, 5*time.Second, nil)
}

func newClientWith(name string, args []string, extra func(*flag.FlagSet)) (*client, error) {
	return newClientWithTimeout(name, args, 5*time.Second, extra)
}

func newClientWithTimeout(name string, args []string, defaultTimeout time.Duration, extra func(*flag.FlagSet)) (*client, error) {
	fileConfig, err := readCLIConfig()
	if err != nil {
		return nil, err
	}
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	addr := fs.String("addr", envOr("ATLAS_ADDR", envOrDefault(fileConfig.Addr, "http://127.0.0.1:8080")), "node API base URL")
	token := fs.String("token", envOr("ATLAS_TOKEN", fileConfig.Token), "API bearer token (prefer ATLAS_TOKEN to keep it out of shell history)")
	ca := fs.String("ca", envOr("ATLAS_CA_FILE", fileConfig.CAFile), "CA bundle for HTTPS")
	cert := fs.String("cert", envOr("ATLAS_CLIENT_CERT", fileConfig.ClientCert), "client certificate (mTLS)")
	key := fs.String("key", envOr("ATLAS_CLIENT_KEY", fileConfig.ClientKey), "client key (mTLS)")
	timeout := fs.Duration("timeout", defaultTimeout, "request timeout")
	if extra != nil {
		extra(fs)
	}
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

func readCLIConfig() (cliConfigFile, error) {
	path := os.Getenv("ATLAS_CLI_CONFIG")
	if path == "" {
		return cliConfigFile{}, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return cliConfigFile{}, fmt.Errorf("open ATLAS_CLI_CONFIG: %w", err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil {
		return cliConfigFile{}, fmt.Errorf("read ATLAS_CLI_CONFIG: %w", err)
	}
	if len(data) > 64<<10 {
		return cliConfigFile{}, errors.New("ATLAS_CLI_CONFIG exceeds 64 KiB")
	}
	var cfg cliConfigFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return cliConfigFile{}, fmt.Errorf("parse ATLAS_CLI_CONFIG: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return cliConfigFile{}, errors.New("ATLAS_CLI_CONFIG must contain one JSON object")
	}
	return cfg, nil
}

func envOrDefault(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func (c *client) stream(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.http.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.addr, "/")+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		return fmt.Errorf("%s: %s %s", path, resp.Status, body)
	}
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			fmt.Println(strings.TrimPrefix(line, "data: "))
		}
	}
	return scanner.Err()
}

func (c *client) get(path string, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.http.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.addr, "/")+path, nil)
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

func services(args []string, requireName bool) error {
	positionalName := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		positionalName, args = args[0], args[1:]
	}
	name, region, health, key, output := "", "", "", "", "table"
	c, err := newClientWith("services", args, func(fs *flag.FlagSet) {
		fs.StringVar(&name, "name", positionalName, "filter by service name")
		fs.StringVar(&region, "region", "", "filter by region")
		fs.StringVar(&health, "health", "", "filter by health (healthy|unhealthy|unknown)")
		fs.StringVar(&key, "key", "", "stable routing key (lookup only)")
		fs.StringVar(&output, "output", "table", "output format (table|json)")
	})
	if err != nil {
		return err
	}
	if requireName && name == "" {
		return errors.New("lookup requires a service name (positional or --name)")
	}
	if output != "table" && output != "json" {
		return errors.New("--output must be table or json")
	}
	if requireName && health != "" {
		return errors.New("--health is available on services, not lookup")
	}
	query := url.Values{}
	if name != "" && !requireName {
		query.Set("name", name)
	}
	if region != "" {
		query.Set("region", region)
	}
	if health != "" && !requireName {
		query.Set("health", health)
	}
	path := "/v1/services"
	if requireName {
		path = "/v1/lookup/" + url.PathEscape(name)
		if key != "" {
			query.Set("key", key)
		}
	} else if key != "" {
		return errors.New("--key is only supported by lookup")
	}
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var resp struct {
		Count    int               `json:"count"`
		Services []storage.Service `json:"services"`
	}
	if requireName {
		var service storage.Service
		if err := c.get(path, &service); err != nil {
			return err
		}
		resp.Count, resp.Services = 1, []storage.Service{service}
	} else if err := c.get(path, &resp); err != nil {
		return err
	}
	if output == "json" {
		if requireName {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(resp.Services[0])
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(resp)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tNODE\tADDRESS\tREGION\tHEALTH\tVERSION\tTAGS")
	for _, service := range resp.Services {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", service.ID, service.Name, service.NodeID, service.Address, service.Region, service.Health, service.Version, strings.Join(service.Tags, ","))
	}
	return tw.Flush()
}

func watch(args []string) error {
	c, err := newClientWithTimeout("watch", args, 24*time.Hour, nil)
	if err != nil {
		return err
	}
	return c.stream("/v1/events")
}

func dbBackup(args []string) error {
	fs := flag.NewFlagSet("db-backup", flag.ExitOnError)
	database := fs.String("db", envOr("ATLAS_DB_PATH", "data/atlas.db"), "source SQLite database path")
	output := fs.String("output", "", "new snapshot path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *output == "" {
		return errors.New("db-backup requires -output")
	}
	if _, err := os.Stat(*database); err != nil {
		return fmt.Errorf("database %q: %w", *database, err)
	}
	store, err := storage.Open(context.Background(), *database)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	if err := store.Backup(context.Background(), *output); err != nil {
		return err
	}
	abs, err := filepath.Abs(*output)
	if err != nil {
		return err
	}
	fmt.Println(abs)
	return nil
}

func dbRestore(args []string) error {
	fs := flag.NewFlagSet("db-restore", flag.ExitOnError)
	input := fs.String("input", "", "snapshot created by db-backup")
	destination := fs.String("db", envOr("ATLAS_DB_PATH", "data/atlas.db"), "new restored database path; must not exist")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *input == "" {
		return errors.New("db-restore requires -input")
	}
	if err := storage.Restore(context.Background(), *input, *destination); err != nil {
		return err
	}
	abs, err := filepath.Abs(*destination)
	if err != nil {
		return err
	}
	fmt.Println(abs)
	return nil
}

func completion(args []string) error {
	if len(args) != 1 {
		return errors.New("completion requires one shell: bash, zsh, fish, or powershell")
	}
	commands := "members services lookup watch db-backup db-restore node health keygen tokengen version completion"
	var script string
	switch args[0] {
	case "bash":
		script = `_atlas_cli_complete() {
  COMPREPLY=( $(compgen -W "` + commands + `" -- "${COMP_WORDS[COMP_CWORD]}") )
}
complete -F _atlas_cli_complete atlas-cli
`
	case "zsh":
		script = `#compdef atlas-cli
_arguments '1:command:(` + commands + `)' '*:argument:'
`
	case "fish":
		for _, command := range strings.Fields(commands) {
			script += "complete -c atlas-cli -f -n '__fish_use_subcommand' -a '" + command + "'\n"
		}
	case "powershell":
		script = `Register-ArgumentCompleter -Native -CommandName atlas-cli -ScriptBlock {
  param($wordToComplete)
  @('` + strings.ReplaceAll(commands, " ", "','") + `') | Where-Object { $_ -like "$wordToComplete*" }
}
`
	default:
		return errors.New("unsupported shell; choose bash, zsh, fish, or powershell")
	}
	fmt.Print(script)
	return nil
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
