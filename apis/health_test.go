package apis_test

import (
	"net/http"
	"testing"

	"github.com/joansebgarcia/amber/tests"
)

func TestHealth(t *testing.T) {
	cases := []tests.Case{
		{
			Name:            "healthy instance returns ok",
			Method:          http.MethodGet,
			Path:            "/api/health",
			ExpectedStatus:  http.StatusOK,
			ExpectedHeaders: map[string]string{"Content-Type": "application/json"},
			ExpectedBody:    `{"status":"ok"}`,
		},
		{
			Name:           "other methods are not allowed",
			Method:         http.MethodPost,
			Path:           "/api/health",
			ExpectedStatus: http.StatusMethodNotAllowed,
		},
	}

	for _, c := range cases {
		c.Run(t)
	}
}
