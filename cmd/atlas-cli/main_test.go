package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShellCompletionScriptsListCommands(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		t.Run(shell, func(t *testing.T) {
			old := os.Stdout
			read, write, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			os.Stdout = write
			err = completion([]string{shell})
			_ = write.Close()
			os.Stdout = old
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if _, err := output.ReadFrom(read); err != nil {
				t.Fatal(err)
			}
			_ = read.Close()
			if !strings.Contains(output.String(), "db-backup") || !strings.Contains(output.String(), "lookup") {
				t.Fatalf("%s completion script missing commands: %s", shell, output.String())
			}
		})
	}
}

func TestCLIConfigFileAndEnvironmentOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atlas-cli.json")
	if err := os.WriteFile(path, []byte(`{"addr":"https://node.example","token":"file-token","ca_file":""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATLAS_CLI_CONFIG", path)
	t.Setenv("ATLAS_ADDR", "https://override.example")
	t.Setenv("ATLAS_TOKEN", "")
	t.Setenv("ATLAS_CA_FILE", "")
	t.Setenv("ATLAS_CLIENT_CERT", "")
	t.Setenv("ATLAS_CLIENT_KEY", "")
	c, err := newClient("test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.addr != "https://override.example" || c.token != "file-token" {
		t.Fatalf("client config = addr %q token %q", c.addr, c.token)
	}
}

func TestCLIConfigRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atlas-cli.json")
	if err := os.WriteFile(path, []byte(`{"address":"https://node.example"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATLAS_CLI_CONFIG", path)
	if _, err := readCLIConfig(); err == nil {
		t.Fatal("unknown config field was accepted")
	}
}
