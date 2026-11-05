package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"autonexus/internal/desktop"
)

const desktopWindowTitle = "AutoNexus 自动化总控平台"

func bindBeforeRuntime(address string, initialize func(net.Listener) error) (net.Listener, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	if initialize != nil {
		if err := initialize(listener); err != nil {
			_ = listener.Close()
			return nil, err
		}
	}
	return listener, nil
}

func isAutoNexusService(client *http.Client, baseURL string) bool {
	if client == nil {
		client = &http.Client{Timeout: 800 * time.Millisecond}
	}
	if client.CheckRedirect == nil {
		copyClient := *client
		copyClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
		client = &copyClient
	}
	root, err := getStartupProbe(client, strings.TrimRight(baseURL, "/")+"/")
	if err != nil || !hasAutoNexusTitle(root) {
		return false
	}
	status, err := getStartupProbe(client, strings.TrimRight(baseURL, "/")+"/api/v1/status")
	return err == nil && validAutoNexusStatus(status)
}

func getStartupProbe(client *http.Client, url string) ([]byte, error) {
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	// No cookies, auth token, or user-provided headers are sent during identity checks.
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	return body, nil
}

func hasAutoNexusTitle(html []byte) bool {
	lower := strings.ToLower(string(html))
	start := strings.Index(lower, "<title>")
	if start < 0 {
		return false
	}
	start += len("<title>")
	end := strings.Index(lower[start:], "</title>")
	return end >= 0 && strings.Contains(lower[start:start+end], "autonexus")
}

func validAutoNexusStatus(data []byte) bool {
	var status map[string]json.RawMessage
	if err := json.Unmarshal(data, &status); err != nil {
		return false
	}
	var state string
	if raw, ok := status["state"]; !ok || json.Unmarshal(raw, &state) != nil {
		return false
	}
	switch state {
	case "IDLE", "PREPARE", "RUNNING", "WAITING_SCHEDULE", "COOLING", "STOPPING":
	default:
		return false
	}
	var tasks []json.RawMessage
	if raw, ok := status["tasks"]; !ok || json.Unmarshal(raw, &tasks) != nil || tasks == nil {
		return false
	}
	// These always-present counters distinguish the current AutoNexus status
	// contract even while the optional current_task_id is empty in IDLE state.
	for _, key := range []string{"current_task_index", "total_tasks_count"} {
		raw, ok := status[key]
		var count int
		if !ok || json.Unmarshal(raw, &count) != nil || count < 0 {
			return false
		}
	}
	if raw, ok := status["current_task_id"]; ok {
		var id string
		if json.Unmarshal(raw, &id) != nil {
			return false
		}
	}
	return true
}

func startupNotice(message string, noUI bool) error {
	if noUI {
		return fmt.Errorf("%s", message)
	}
	desktop.ShowStartupError("AutoNexus 启动提示", message)
	return nil
}
