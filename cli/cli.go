package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type command struct {
	method string
	path   string
	body   []byte
}

func Run(arguments []string, output, errorOutput io.Writer) error {
	flags := flag.NewFlagSet("factory", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	baseURL := flags.String("url", defaultURL(), "Factory server URL")
	flags.Usage = func() { usage(errorOutput) }
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	args := flags.Args()
	if len(args) == 0 || args[0] == "help" {
		usage(output)
		return nil
	}
	request, err := parse(args)
	if err != nil {
		return err
	}
	endpoint, err := url.JoinPath(strings.TrimRight(*baseURL, "/"), request.path)
	if err != nil {
		return fmt.Errorf("build Factory URL: %w", err)
	}
	httpRequest, err := http.NewRequest(request.method, endpoint, bytes.NewReader(request.body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	if len(request.body) > 0 {
		httpRequest.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		return fmt.Errorf("contact Factory: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return fmt.Errorf("read Factory response: %w", err)
	}
	if response.StatusCode >= 400 {
		return fmt.Errorf("Factory returned %s: %s", response.Status, strings.TrimSpace(string(data)))
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	var formatted bytes.Buffer
	if json.Indent(&formatted, data, "", "  ") == nil {
		data = formatted.Bytes()
	}
	_, err = fmt.Fprintln(output, string(data))
	return err
}

func parse(args []string) (command, error) {
	if len(args) < 2 {
		return command{}, errors.New("resource and action are required")
	}
	resource, action := args[0], args[1]
	if resource == "settings" {
		switch {
		case action == "get" && len(args) == 2:
			return command{method: http.MethodGet, path: "/api/settings"}, nil
		case action == "update":
			body, err := argumentJSON(args, 2)
			return command{method: http.MethodPut, path: "/api/settings", body: body}, err
		default:
			return command{}, errors.New("usage: factory settings get|update <json|@file>")
		}
	}
	plural, found := map[string]string{
		"project": "projects", "task": "tasks", "comment": "comments",
		"artifact": "artifacts", "event": "events", "trigger": "triggers", "workflow": "workflows",
	}[resource]
	if !found {
		return command{}, fmt.Errorf("unknown resource %q", resource)
	}
	switch action {
	case "list":
		if len(args) != 2 || resource == "comment" {
			return command{}, fmt.Errorf("usage: factory %s list", resource)
		}
		return command{method: http.MethodGet, path: "/api/" + plural}, nil
	case "get":
		if len(args) != 3 {
			return command{}, fmt.Errorf("usage: factory %s get <id>", resource)
		}
		id, err := argumentID(args, 2)
		if err != nil {
			return command{}, err
		}
		return command{method: http.MethodGet, path: "/api/" + plural + "/" + id}, nil
	case "create":
		if resource == "comment" {
			return command{}, errors.New("comments are created through task comment or workflow comment")
		}
		body, err := argumentJSON(args, 2)
		if err != nil {
			return command{}, err
		}
		return command{method: http.MethodPost, path: "/api/" + plural, body: body}, nil
	case "update":
		if resource == "event" || len(args) != 4 {
			return command{}, fmt.Errorf("usage: factory %s update <id> <json|@file>", resource)
		}
		id, err := argumentID(args, 2)
		if err != nil {
			return command{}, err
		}
		body, err := argumentJSON(args, 3)
		if err != nil {
			return command{}, err
		}
		return command{method: http.MethodPut, path: "/api/" + plural + "/" + id, body: body}, nil
	case "delete":
		if resource == "event" || len(args) != 3 {
			return command{}, fmt.Errorf("usage: factory %s delete <id>", resource)
		}
		id, err := argumentID(args, 2)
		if err != nil {
			return command{}, err
		}
		return command{method: http.MethodDelete, path: "/api/" + plural + "/" + id}, nil
	case "comment":
		if resource != "task" && resource != "workflow" {
			return command{}, fmt.Errorf("%s does not accept comments", resource)
		}
		if len(args) != 4 {
			return command{}, fmt.Errorf("usage: factory %s comment <id> <json|@file>", resource)
		}
		id, err := argumentID(args, 2)
		if err != nil {
			return command{}, err
		}
		body, err := argumentJSON(args, 3)
		if err != nil {
			return command{}, err
		}
		return command{
			method: http.MethodPost, path: "/api/" + plural + "/" + id + "/comments", body: body,
		}, nil
	default:
		return command{}, fmt.Errorf("unknown %s action %q", resource, action)
	}
}

func argumentID(args []string, position int) (string, error) {
	if len(args) <= position {
		return "", errors.New("an integer ID is required")
	}
	id, err := strconv.ParseInt(args[position], 10, 64)
	if err != nil || id < 1 {
		return "", errors.New("ID must be a positive integer")
	}
	return strconv.FormatInt(id, 10), nil
}

func argumentJSON(args []string, position int) ([]byte, error) {
	if len(args) != position+1 {
		return nil, errors.New("a JSON object or @file is required")
	}
	value := args[position]
	var data []byte
	var err error
	if strings.HasPrefix(value, "@") {
		data, err = os.ReadFile(strings.TrimPrefix(value, "@"))
		if err != nil {
			return nil, fmt.Errorf("read JSON file: %w", err)
		}
	} else {
		data = []byte(value)
	}
	if !json.Valid(data) {
		return nil, errors.New("body must be valid JSON")
	}
	return data, nil
}

func defaultURL() string {
	if value := os.Getenv("FACTORY_URL"); value != "" {
		return value
	}
	return "http://127.0.0.1:8092"
}

func usage(output io.Writer) {
	fmt.Fprintln(output, `Factory resource CLI

Usage:
  factory [--url URL] <resource> <action> [id] [json|@file]

Resources:
  project   list, get, create, update, delete
  task      list, get, create, update, delete, comment
  comment   get, update, delete
  artifact  list, get, create, update, delete
  event     list, get, create
  trigger   list, get, create, update, delete
  workflow  list, get, create, update, delete, comment
  settings  get, update

Examples:
  factory project create '{"name":"Factory","path":"/path/to/factory"}'
  factory task create '{"title":"Review the PR","status":"todo","projectId":1}'
  factory task comment 12 '{"content":"The build passed."}'
  factory artifact get 18
  factory workflow create '{"message":"Build a review-panel workflow."}'
  factory workflow update 24 '{"message":"Add a security reviewer."}'
  factory settings update '{"harness":"claude","model":"sonnet","reasoning":"high"}'`)
}
