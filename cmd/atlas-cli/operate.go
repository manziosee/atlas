package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/manziosee/atlas/internal/storage"
)

// stringList is a repeatable string flag.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

// listAll follows next_cursor until the listing is complete.
func (c *client) listAll(query url.Values) ([]storage.Service, error) {
	all := make([]storage.Service, 0)
	cursor := ""
	for {
		pageQuery := url.Values{}
		for key, values := range query {
			pageQuery[key] = values
		}
		pageQuery.Set("limit", "1000")
		if cursor != "" {
			pageQuery.Set("cursor", cursor)
		}
		var page struct {
			Services   []storage.Service `json:"services"`
			NextCursor string            `json:"next_cursor"`
		}
		if err := c.get("/v1/services?"+pageQuery.Encode(), &page); err != nil {
			return nil, err
		}
		all = append(all, page.Services...)
		if page.NextCursor == "" {
			return all, nil
		}
		cursor = page.NextCursor
	}
}

// send performs a non-GET request and decodes a JSON response into out.
func (c *client) send(method, path string, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.http.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.addr, "/")+path, nil)
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
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apiError(path, resp.Status, body)
	}
	if out == nil || len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, out)
}

// apiError formats the server's JSON error body (message, code, owner).
func apiError(path, status string, body []byte) error {
	var decoded struct {
		Error     string `json:"error"`
		Code      string `json:"code"`
		OwnerNode string `json:"owner_node"`
	}
	if json.Unmarshal(body, &decoded) != nil || decoded.Error == "" {
		return fmt.Errorf("%s: %s %s", path, status, strings.TrimSpace(string(body)))
	}
	message := fmt.Sprintf("%s: %s: %s (%s)", path, status, decoded.Error, decoded.Code)
	if decoded.OwnerNode != "" {
		message += "; owner node: " + decoded.OwnerNode
	}
	return errors.New(message)
}

// positional splits a required leading argument from the flags after it.
func positional(command string, args []string, what string) (string, []string, error) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return "", nil, fmt.Errorf("%s requires a %s", command, what)
	}
	return args[0], args[1:], nil
}

func heartbeat(args []string) error {
	id, rest, err := positional("heartbeat", args, "service ID")
	if err != nil {
		return err
	}
	c, err := newClient("heartbeat", rest)
	if err != nil {
		return err
	}
	var service storage.Service
	if err := c.send(http.MethodPost, "/v1/services/"+url.PathEscape(id)+"/heartbeat", &service); err != nil {
		return err
	}
	fmt.Printf("%s %s ttl=%ds\n", service.ID, service.Health, service.TTLSeconds)
	return nil
}

func deregister(args []string) error {
	id, rest, err := positional("deregister", args, "service ID")
	if err != nil {
		return err
	}
	c, err := newClient("deregister", rest)
	if err != nil {
		return err
	}
	if err := c.send(http.MethodDelete, "/v1/services/"+url.PathEscape(id), nil); err != nil {
		return err
	}
	fmt.Println("deregistered", id)
	return nil
}

func credentials(args []string) error {
	c, err := newClient("credentials", args)
	if err != nil {
		return err
	}
	var resp struct {
		Credentials []struct {
			ID        string    `json:"id"`
			Scopes    []string  `json:"scopes"`
			ExpiresAt time.Time `json:"expires_at"`
			Source    string    `json:"source"`
			Expired   bool      `json:"expired"`
			Current   bool      `json:"current"`
		} `json:"credentials"`
	}
	if err := c.get("/v1/admin/credentials", &resp); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSCOPES\tSOURCE\tEXPIRES\tSTATUS")
	for _, credential := range resp.Credentials {
		expires, state := "never", "active"
		if !credential.ExpiresAt.IsZero() {
			expires = credential.ExpiresAt.Format(time.RFC3339)
		}
		if credential.Expired {
			state = "expired"
		}
		if credential.Current {
			state += " (this token)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", credential.ID, strings.Join(credential.Scopes, ","), credential.Source, expires, state)
	}
	return tw.Flush()
}

func remoteBackup(args []string) error {
	output := ""
	c, err := newClientWithTimeout("backup", args, 5*time.Minute, func(fs *flag.FlagSet) {
		fs.StringVar(&output, "output", "", "file to create with the node's SQLite snapshot")
	})
	if err != nil {
		return err
	}
	if output == "" {
		return errors.New("backup requires -output")
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.http.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.addr, "/")+"/v1/admin/backup", nil)
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
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return apiError("/v1/admin/backup", resp.Status, body)
	}
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- operator-selected output path.
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(file, resp.Body)
	if err := errors.Join(copyErr, file.Close()); err != nil {
		_ = os.Remove(output)
		return err
	}
	fmt.Printf("%s (%d bytes)\n", output, written)
	return nil
}

func evict(args []string) error {
	nodeID, rest, err := positional("evict", args, "node ID")
	if err != nil {
		return err
	}
	c, err := newClient("evict", rest)
	if err != nil {
		return err
	}
	var result struct {
		ServicesRemoved int `json:"services_removed"`
	}
	if err := c.send(http.MethodDelete, "/v1/admin/nodes/"+url.PathEscape(nodeID), &result); err != nil {
		return err
	}
	fmt.Printf("evicted %s; deleted %d services\n", nodeID, result.ServicesRemoved)
	return nil
}
