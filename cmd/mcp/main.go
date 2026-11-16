// Command mcp is a Model Context Protocol server over stdio that lets an AI
// agent use the emtds admin HTTP API with an API key.
//
// Environment:
//
//	EMTDS_URL        admin base URL, for example https://212.2.33.246
//	EMTDS_API_KEY    API key sent in the Api-Key header (never logged)
//	EMTDS_CA_FILE    optional PEM file with the CA of a self-signed admin certificate
//	EMTDS_MCP_WRITE  set to 1 to enable the write tools
//	EMTDS_MCP_FORMAT json (default) or toon: the format of the tool results
//
// Each EMTDS_ variable falls back to the same KUZTDS_ name when it is empty.
package main

import (
	"errors"
	"io"
	"log"
	"os"
	"strings"

	envconfig "github.com/egerkuzma/emtds/internal/config"
)

// config is the server configuration read from the environment.
type config struct {
	URL    string
	APIKey string
	CAFile string
	Write  bool
	TOON   bool
}

// loadConfig reads the configuration through getenv.
// It fails when EMTDS_URL or EMTDS_API_KEY is empty, or EMTDS_MCP_FORMAT is
// set to something other than json or toon.
func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{
		URL:    getenv("EMTDS_URL"),
		APIKey: getenv("EMTDS_API_KEY"),
		CAFile: getenv("EMTDS_CA_FILE"),
		Write:  getenv("EMTDS_MCP_WRITE") == "1",
	}
	if cfg.URL == "" {
		return cfg, errors.New("EMTDS_URL is required")
	}
	if cfg.APIKey == "" {
		return cfg, errors.New("EMTDS_API_KEY is required")
	}
	switch strings.ToLower(strings.TrimSpace(getenv("EMTDS_MCP_FORMAT"))) {
	case "", "json":
	case "toon":
		cfg.TOON = true
	default:
		return cfg, errors.New("EMTDS_MCP_FORMAT must be json or toon")
	}
	return cfg, nil
}

// run starts the server: it reads the configuration, downloads the OpenAPI
// document and serves MCP on stdin/stdout. Logs go to stderr only.
// It returns the process exit code: 0 after stdin closes, 1 on a serving
// error, 2 on a configuration or startup error.
func run(getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) int {
	logger := log.New(stderr, "emtds-mcp: ", 0)
	cfg, err := loadConfig(getenv)
	if err != nil {
		logger.Print(err)
		return 2
	}
	client, err := newAPIClient(cfg)
	if err != nil {
		logger.Print(err)
		return 2
	}
	tools, err := client.loadTools()
	if err != nil {
		logger.Printf("startup failed: %s", client.redact(err.Error()))
		return 2
	}
	srv := newServer(tools, cfg.Write, client, logger)
	logger.Printf("%d tools from %s, writes enabled: %t, toon: %t", len(srv.visibleTools()), client.base, cfg.Write, cfg.TOON)
	if err := srv.serve(stdin, stdout); err != nil {
		logger.Print(err)
		return 1
	}
	return 0
}

// main runs the server and exits with its code.
func main() {
	os.Exit(run(envconfig.Env, os.Stdin, os.Stdout, os.Stderr))
}
