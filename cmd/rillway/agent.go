package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net"
	"os"
	"reflect"
	"rillway/internal/buildinfo"
	"rillway/internal/config"
	"rillway/internal/control"
	"strings"
	"time"
)

// agentFailure has already been written as JSON. main must not print it again.
type agentFailure struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	ExitCode int    `json:"exit_code"`
}

func (e *agentFailure) Error() string { return e.Message }

type agentResult struct {
	SchemaVersion int           `json:"schema_version"`
	OK            bool          `json:"ok"`
	Data          any           `json:"data,omitempty"`
	Error         *agentFailure `json:"error,omitempty"`
}

func agentFail(code, message string, exit int) *agentFailure {
	return &agentFailure{Code: code, Message: message, ExitCode: exit}
}

func agentCommand(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
	data, err := agentOperation(ctx, args, in)
	return writeAgentResult(ctx, data, err, out)
}

func writeAgentResult(ctx context.Context, data any, err error, out io.Writer) error {
	result := agentResult{SchemaVersion: 1, OK: err == nil, Data: data}
	if err != nil {
		var failure *agentFailure
		if !errors.As(err, &failure) {
			failure = classifyAgentError(err)
		}
		// Stable codes and keys never depend on the display language.
		result.Error = &agentFailure{Code: failure.Code, Message: cliText(ctx, failure.Message), ExitCode: failure.ExitCode}
		err = result.Error
	}
	if writeErr := json.NewEncoder(out).Encode(result); writeErr != nil {
		return agentFail("output_failed", "Could not write JSON output.", 1)
	}
	return err
}

func classifyAgentError(err error) *agentFailure {
	var api *control.APIError
	if errors.As(err, &api) {
		switch api.Status {
		case 401, 403:
			return agentFail("access_denied", "Management access denied. Check the token and allowed client address.", 3)
		case 409:
			return agentFail("revision_conflict", "Configuration changed elsewhere. Fetch it again and review a new plan.", 4)
		default:
			return agentFail("operation_rejected", "The server rejected the operation. Check settings in the Web UI.", 1)
		}
	}
	return agentFail("connection_failed", "Management connection failed. Check the service, address and trusted certificate. For writes, inspect state before retrying.", 5)
}

func agentOperation(ctx context.Context, args []string, in io.Reader) (any, error) {
	if len(args) == 0 {
		return nil, agentFail("invalid_arguments", "Use rillway agent schema to discover commands.", 2)
	}
	command := args[0]
	offline := command == "schema" || command == "validate"
	inputCommand := command == "validate" || command == "plan" || command == "apply"
	mutation := command == "apply" || command == "restart" || command == "outbound"
	switch command {
	case "schema", "validate", "status", "config", "stats", "plan", "apply", "restart", "outbound":
	default:
		return nil, agentFail("invalid_arguments", "Use rillway agent schema to discover commands.", 2)
	}
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var path, base, tokenPath, ca, input, id, action string
	var yes bool
	timeout := 15 * time.Second
	if !offline {
		fs.StringVar(&path, "config", defaultPath(), "local configuration")
		fs.StringVar(&base, "url", "", "explicit management HTTPS URL")
		fs.StringVar(&tokenPath, "token-file", "", "private token file")
		fs.StringVar(&ca, "ca", "", "trusted certificate PEM")
		fs.DurationVar(&timeout, "timeout", timeout, "overall deadline, at most 2m")
	}
	if inputCommand {
		fs.StringVar(&input, "input", "", "candidate JSON file, or - for stdin")
	}
	if mutation {
		fs.BoolVar(&yes, "yes", false, "explicitly authorize this operation")
	}
	if command == "outbound" {
		fs.StringVar(&id, "id", "", "outbound ID")
		fs.StringVar(&action, "action", "", "connect, disconnect or verify")
	}
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 || timeout <= 0 || timeout > 2*time.Minute {
		return nil, agentFail("invalid_arguments", "Invalid arguments. Use rillway agent schema.", 2)
	}
	if command == "schema" {
		return agentSchema(), nil
	}
	if mutation && !yes {
		return nil, agentFail("confirmation_required", "This operation requires --yes. Review its effects first.", 2)
	}
	if command == "outbound" && (id == "" || strings.ContainsAny(id, "/?#\r\n") || (action != "connect" && action != "disconnect" && action != "verify")) {
		return nil, agentFail("invalid_arguments", "Choose an outbound ID and connect, disconnect or verify.", 2)
	}
	var candidate config.Config
	if inputCommand {
		data, err := agentRead(input, in, 256<<10)
		if err != nil {
			return nil, agentFail("invalid_input", "Provide one configuration JSON object with --input FILE or --input - (maximum 256 KiB).", 2)
		}
		candidate, err = config.Decode(data)
		if err != nil {
			// Parser/validation errors may quote submitted credentials or paths.
			return nil, agentFail("invalid_configuration", "Configuration validation failed. Check the documented configuration fields.", 2)
		}
		if command == "validate" {
			return map[string]any{"valid": true, "revision": candidate.Revision, "runtime_verified": false}, nil
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client, err := agentClient(path, base, tokenPath, ca)
	if err != nil {
		return nil, err
	}
	switch command {
	case "config":
		return client.Config(ctx)
	case "stats":
		return client.Snapshot(ctx)
	case "status":
		c, err := client.Config(ctx)
		if err != nil {
			return nil, err
		}
		statuses, err := client.Statuses(ctx)
		if err != nil {
			return nil, err
		}
		// Do not expose interactive login URLs or free-form provider output.
		for i := range statuses {
			statuses[i].AuthURL = ""
			statuses[i].Detail = ""
		}
		return map[string]any{"config_revision": c.Revision, "adaptive_enabled": c.Adaptive.Enabled, "outbounds": statuses}, nil
	case "plan", "apply":
		current, err := client.Config(ctx)
		if err != nil {
			return nil, err
		}
		if candidate.Revision != current.Revision {
			return nil, agentFail("revision_conflict", "Configuration changed elsewhere. Fetch it again and review a new plan.", 4)
		}
		if !reflect.DeepEqual(current.Listeners, candidate.Listeners) || !reflect.DeepEqual(current.Security, candidate.Security) {
			return nil, agentFail("server_settings_required", "Edit listener and security settings on the server, then restart the service.", 2)
		}
		fields := changedAgentFields(current, candidate)
		if command == "plan" {
			return map[string]any{"revision": current.Revision, "changed_fields": fields, "would_change": len(fields) > 0, "runtime_verified": false}, nil
		}
		if len(fields) == 0 {
			return map[string]any{"revision": current.Revision, "changed": false}, nil
		}
		applied, err := client.Apply(ctx, candidate)
		if err != nil {
			return nil, err
		}
		return map[string]any{"revision": applied.Revision, "changed": true}, nil
	case "restart":
		if err := client.Restart(ctx); err != nil {
			return nil, err
		}
		return map[string]any{"accepted": true, "active_connections_close": true}, nil
	case "outbound":
		if err := client.Action(ctx, id, action, ""); err != nil {
			return nil, err
		}
		return map[string]any{"accepted": true}, nil
	}
	return nil, agentFail("invalid_arguments", "Use rillway agent schema to discover commands.", 2)
}

func agentRead(path string, in io.Reader, limit int64) ([]byte, error) {
	if path == "" || path == "-" && in == nil {
		return nil, errors.New("missing input")
	}
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer func() { _ = f.Close() }()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			return nil, errors.New("input is not a regular file")
		}
		in = f
	}
	data, err := io.ReadAll(io.LimitReader(in, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("input could not be read within the limit")
	}
	return data, nil
}

func agentClient(path, base, tokenPath, ca string) (*control.Client, error) {
	if base == "" {
		data, err := agentRead(path, nil, 1<<20)
		if err != nil {
			return nil, agentFail("local_configuration_unavailable", "Cannot read local configuration. Select --config or an explicit remote --url and --token-file.", 2)
		}
		c, err := config.Decode(data)
		if err != nil {
			return nil, agentFail("invalid_configuration", "Configuration validation failed. Check the documented configuration fields.", 2)
		}
		base = "https://" + c.Listeners.Admin
		if host, port, _ := net.SplitHostPort(c.Listeners.Admin); host == "" || host == "0.0.0.0" || host == "::" {
			base = "https://" + net.JoinHostPort("localhost", port)
		}
		if tokenPath == "" {
			tokenPath = c.Security.AdminTokenFile
		}
		if ca == "" {
			ca = c.Security.TLSCertFile
		}
	}
	token, err := agentRead(tokenPath, nil, 4096)
	value := strings.TrimSpace(string(token))
	if err != nil || value == "" || strings.ContainsAny(value, "\r\n\x00") {
		return nil, agentFail("credentials_unavailable", "Cannot read a valid management token from --token-file or the local configuration.", 3)
	}
	client, err := control.NewClient(base, value, ca)
	if err != nil {
		return nil, agentFail("invalid_connection", "Invalid management URL or trusted certificate. Remote management requires HTTPS.", 2)
	}
	return client, nil
}

func changedAgentFields(current, candidate config.Config) []string {
	fields := make([]string, 0)
	a, b := reflect.ValueOf(current), reflect.ValueOf(candidate)
	for i := range a.NumField() {
		if !reflect.DeepEqual(a.Field(i).Interface(), b.Field(i).Interface()) {
			fields = append(fields, strings.Split(a.Type().Field(i).Tag.Get("json"), ",")[0])
		}
	}
	return fields
}

func agentSchema() any {
	return map[string]any{
		"binary_version": buildinfo.Version, "contract_version": 1,
		"commands": map[string]string{
			"schema":   "Offline command discovery; no configuration or credentials needed.",
			"validate": "Offline syntax and configuration validation; no provider or network checks.",
			"status":   "Effective configuration revision and provider states; no login URLs or raw details.",
			"config":   "Configuration with file references only; includes private infrastructure data.",
			"stats":    "Connection observations; includes private destinations. Does not generate traffic.",
			"plan":     "Validate a candidate, compare current revision, list changed top-level fields. No writes or provider preflight.",
			"apply":    "Apply a candidate with its expected revision. Requires --yes; unchanged candidates do not write.",
			"restart":  "Restart listeners/providers in this daemon. Requires --yes; closes active connections, not a binary upgrade.",
			"outbound": "Explicit connect, disconnect or end-to-end verify. Requires --id, --action and --yes. May change the official WARP client.",
		},
		"connection_flags": map[string]string{"config": "OS default local config; never initialized by agent commands", "url": "explicit remote HTTPS URL; never select a remembered TUI profile", "token-file": "private token file; no inline token option", "ca": "trusted PEM certificate; TLS verification cannot be disabled", "timeout": "network operation deadline after reading input (default 15s, range >0 to 2m); HTTP request limit 15s"},
		"input":            map[string]any{"commands": []string{"validate", "plan", "apply"}, "flag": "--input FILE or --input -", "format": "one complete Config v1 JSON object, not the CLI result envelope", "maximum_bytes": 256 << 10, "expected_revision": "revision in the candidate must equal the effective revision; do not invent or increment it", "example_source": "agent config returns the candidate in data; preserve every field before editing"},
		"output":           map[string]any{"stream": "stdout: exactly one JSON object on success or failure; no prompts or ANSI", "schema_version": 1, "success": []string{"schema_version", "ok", "data"}, "failure": []string{"schema_version", "ok", "error.code", "error.message", "error.exit_code"}, "language": "en or zh-Hant via --lang; keys and error codes stay stable"},
		"exit_codes":       map[string]string{"0": "success", "1": "server rejection or output failure", "2": "invalid arguments/configuration or missing --yes", "3": "credentials unavailable or access denied", "4": "revision conflict", "5": "connection/TLS/deadline failure; a write may already have succeeded"},
		"mutation_policy":  "No automatic retries. Re-read state after an ambiguous failure. A plan is not a provider health check or a reservation. Listener/security edits require server-side changes. Registration, licenses and profile import use the existing UI.",
	}
}
