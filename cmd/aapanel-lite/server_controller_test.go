package main

import (
	"net/http"
	"testing"
)

func TestServerControllerRestart(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"running"}`))
	})

	controller := newServerController("127.0.0.1:18080", mux)

	if err := controller.Start(); err != nil {
		t.Fatalf("first start failed: %v", err)
	}
	if err := controller.Stop(); err != nil {
		t.Fatalf("first stop failed: %v", err)
	}
	if err := controller.Start(); err != nil {
		t.Fatalf("second start failed: %v", err)
	}
	if err := controller.Stop(); err != nil {
		t.Fatalf("second stop failed: %v", err)
	}
}
