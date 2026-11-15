package pyroscope

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDeadlineClientResponseBody(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	server := httptest.NewTLSServer(
		http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			response.Header().Set("Content-Length", "8")
			response.WriteHeader(http.StatusOK)
			if err := http.NewResponseController(response).Flush(); err != nil {
				t.Error(err)

				return
			}

			select {
			case <-release:
			case <-request.Context().Done():
				return
			}

			if _, err := io.WriteString(response, "accepted"); err != nil {
				t.Error(err)
			}
		}),
	)
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	client := &deadlineClient{next: server.Client(), timeout: 5 * time.Second}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}

	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := response.Body.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := response.Request.Context().Err(); err != nil {
		t.Fatalf("response context ended before body consumption: %v", err)
	}

	release <- struct{}{}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}

	if string(body) != "accepted" {
		t.Fatalf("response body = %q", body)
	}

	if err := response.Request.Context().Err(); err != nil {
		t.Fatalf("response context ended before body closure: %v", err)
	}

	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}

	if err := response.Request.Context().Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("closed body context = %v", err)
	}
}
