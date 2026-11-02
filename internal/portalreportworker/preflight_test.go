package portalreportworker

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func preflightClient(status int, err error) *projectServiceClient {
	return &projectServiceClient{
		client: &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if err != nil {
				return nil, err
			}
			if req.Method != http.MethodHead {
				return nil, io.ErrUnexpectedEOF
			}
			return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
		})},
		endpoint: "https://project.example/report-requests",
	}
}

func TestProjectPreflightAcceptsReachableEndpoint(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNoContent, http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusUnprocessableEntity} {
		if err := preflightProjectEndpoint(context.Background(), preflightClient(status, nil), "https://project.example/report-requests"); err != nil {
			t.Fatalf("status=%d err=%v, want reachable", status, err)
		}
	}
}

func TestProjectPreflightFailsClosedOnAuthOrUnhealthyDownstream(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		if err := preflightProjectEndpoint(context.Background(), preflightClient(status, nil), "https://project.example/report-requests"); err == nil {
			t.Fatalf("status=%d was accepted", status)
		}
	}
}

func TestProjectPreflightRejectsTransportErrorsAndInvalidConfiguration(t *testing.T) {
	client := preflightClient(0, context.DeadlineExceeded)
	if err := preflightProjectEndpoint(context.Background(), client, "https://project.example/report-requests"); err == nil || err.Error() != "project-service request timed out" {
		t.Fatalf("transport err=%v", err)
	}
	if err := preflightProjectEndpoint(context.Background(), nil, "https://project.example/report-requests"); err == nil {
		t.Fatal("nil client was accepted")
	}
	client = preflightClient(http.StatusOK, nil)
	for _, endpoint := range []string{"", "ftp://project.example/report-requests", "project.example/report-requests", "/relative"} {
		if err := preflightProjectEndpoint(context.Background(), client, endpoint); err == nil {
			t.Fatalf("endpoint %q was accepted", endpoint)
		}
	}
}
