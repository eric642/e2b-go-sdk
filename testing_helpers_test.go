package e2b

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// restMock wraps an httptest.Server with a pre-wired Config whose APIURL
// points at the server. Close is registered via t.Cleanup.
type restMock struct {
	Server *httptest.Server
	Config Config
}

// newRESTMock starts an httptest server with the provided handler and returns
// a Config suitable for e2b.Create / e2b.Connect / e2b.Kill.
func newRESTMock(t *testing.T, h http.Handler) *restMock {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &restMock{
		Server: srv,
		Config: Config{
			APIKey: "test-key",
			APIURL: srv.URL,
			Domain: "example.com",
		},
	}
}
