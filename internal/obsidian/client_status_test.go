package obsidian

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCreateAndAppendAcceptNoContent(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/vault/test.md", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case http.MethodPut, http.MethodPost:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	c := New(srv.URL, "Authorization", "key", false)
	ctx := context.Background()
	if err := c.CreateFile(ctx, "test.md", "hello"); err != nil {
		t.Fatalf("create should accept 204: %v", err)
	}
	if err := c.AppendFile(ctx, "test.md", "world"); err != nil {
		t.Fatalf("append should accept 204: %v", err)
	}
}
