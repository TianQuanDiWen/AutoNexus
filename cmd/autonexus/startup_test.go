package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsAutoNexusService(t *testing.T) {
	tests := []struct {
		name   string
		root   string
		status string
		want   bool
	}{
		{
			name:   "recognizes current status contract",
			root:   `<html><head><title>AutoNexus - 自动化任务批量调度</title></head></html>`,
			status: `{"state":"IDLE","tasks":[],"current_task_index":0,"total_tasks_count":0}`,
			want:   true,
		},
		{
			name:   "rejects unrelated title",
			root:   `<html><head><title>Other Service</title></head></html>`,
			status: `{"state":"IDLE","tasks":[],"current_task_index":0,"total_tasks_count":0}`,
		},
		{
			name:   "rejects status without AutoNexus fields",
			root:   `<html><head><title>AutoNexus</title></head></html>`,
			status: `{"state":"IDLE","tasks":[]}`,
		},
		{
			name:   "rejects unknown state",
			root:   `<html><head><title>AutoNexus</title></head></html>`,
			status: `{"state":"RUNNING_OTHER","tasks":[],"current_task_index":0,"total_tasks_count":0}`,
		},
		{
			name:   "rejects wrong counter type",
			root:   `<html><head><title>AutoNexus</title></head></html>`,
			status: `{"state":"IDLE","tasks":[],"current_task_index":"0","total_tasks_count":0}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Errorf("identity probe must not send user credentials")
				}
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				if r.URL.Path == "/" {
					_, _ = w.Write([]byte(tt.root))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.status))
			}))
			defer server.Close()
			if got := isAutoNexusService(server.Client(), server.URL); got != tt.want {
				t.Fatalf("isAutoNexusService() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestBindBeforeRuntimeDoesNotInitializeOnConflict(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()

	initialized := false
	listener, err := bindBeforeRuntime(occupied.Addr().String(), func(net.Listener) error {
		initialized = true
		return nil
	})
	if err == nil {
		if listener != nil {
			_ = listener.Close()
		}
		t.Fatal("expected binding to the occupied address to fail")
	}
	if initialized {
		t.Fatal("runtime initializer ran despite listener conflict")
	}
}

func TestBindBeforeRuntimeInitializesOnlyAfterListen(t *testing.T) {
	initialized := false
	listener, err := bindBeforeRuntime("127.0.0.1:0", func(net.Listener) error {
		initialized = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if !initialized {
		t.Fatal("runtime initializer did not run after successful bind")
	}
}
