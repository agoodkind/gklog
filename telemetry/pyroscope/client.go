package pyroscope

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/grafana/pyroscope-go/upstream/remote"
)

type deadlineClient struct {
	next    remote.HTTPClient
	timeout time.Duration
}

func (c *deadlineClient) Do(request *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(request.Context(), c.timeout)
	response, err := c.next.Do(request.Clone(ctx))
	if err != nil {
		cancel()

		return response, &operationError{operation: "send profile", err: err}
	}

	response.Body = &deadlineBody{ReadCloser: response.Body, cancel: cancel}

	return response, nil
}

type deadlineBody struct {
	io.ReadCloser

	cancel context.CancelFunc
}

func (b *deadlineBody) Close() error {
	defer b.cancel()
	if err := b.ReadCloser.Close(); err != nil {
		return &operationError{operation: "close profile response", err: err}
	}

	return nil
}
