package presaleengineersyncworker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func sourceWithBody(body string) *HTTPSource {
	return &HTTPSource{endpoint: "https://pms.example/api/v1/pms/technicians", now: func() time.Time { return time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC) }, nonceReader: strings.NewReader(strings.Repeat("n", 32)), client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("scope") != "tech" {
			panic("missing fixed tech scope")
		}
		if r.Header.Get("X-Integration-Timestamp") == "" || r.Header.Get("X-Integration-Nonce") == "" {
			panic("missing integration replay-defense headers")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: ioNopCloser{strings.NewReader(body)}}, nil
	})}}
}

type ioNopCloser struct{ *strings.Reader }

func (ioNopCloser) Close() error { return nil }

func TestHTTPSourceParsesExactT5Snapshot(t *testing.T) {
	source := sourceWithBody(`{"technicians":[{"personId":" P-1 ","personName":"王工","department":"安全部","role":"实施工程师","skillTags":["等保"],"contact":"13800000000","validFlag":true,"syncedAt":"2026-07-23 08:00:00"}]}`)
	got, err := source.Fetch(context.Background(), "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Full || got.TenantID != "tenant-a" || got.Engineers[0].PersonID != "P-1" || !got.Revision.Equal(time.Date(2026, 7, 23, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("snapshot=%#v", got)
	}
}
func TestHTTPSourceRejectsMissingBooleanTrailingJSONAndUnknownField(t *testing.T) {
	cases := []string{`{"technicians":[{"personId":"P","personName":"N","department":"D","role":"实施工程师","skillTags":[],"contact":"","syncedAt":"2026-07-23T08:00:00Z"}]}`, `{"technicians":[]} {}`, `{"technicians":[],"tenantId":"untrusted"}`}
	for _, body := range cases {
		if _, err := sourceWithBody(body).Fetch(context.Background(), "tenant-a"); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}

func TestHTTPSourceRejectsOversizedBodyAndNormalizesSkills(t *testing.T) {
	if _, err := sourceWithBody(strings.Repeat(" ", (4<<20)+1)).Fetch(context.Background(), "tenant-a"); err == nil {
		t.Fatal("oversized body accepted")
	}
	got, err := sourceWithBody(`{"technicians":[{"personId":"P","personName":"N","department":"D","role":"实施工程师","skillTags":[" 等保 "],"contact":"","validFlag":true,"syncedAt":"2026-07-23T08:00:00Z"}]}`).Fetch(context.Background(), "tenant-a")
	if err != nil || got.Engineers[0].SkillTags[0] != "等保" {
		t.Fatalf("snapshot=%#v err=%v", got, err)
	}
}
func TestValidateSnapshotRejectsTenantMismatchEmptyDuplicateUnknownRoleAndOversizedContact(t *testing.T) {
	now := time.Now().UTC()
	valid := SourceSnapshot{TenantID: "t", Full: true, Revision: now, Engineers: []SourceEngineer{{PersonID: "p", PersonName: "n", Department: "d", Role: "实施工程师", ValidFlag: true, SyncedAt: now}}}
	cases := []SourceSnapshot{valid}
	cases[0].TenantID = "other"
	empty := valid
	empty.Engineers = nil
	duplicate := valid
	duplicate.Engineers = append(duplicate.Engineers, duplicate.Engineers[0])
	role := valid
	role.Engineers = []SourceEngineer{{PersonID: "p", PersonName: "n", Department: "d", Role: "未知", SyncedAt: now}}
	contact := valid
	contact.Engineers = []SourceEngineer{{PersonID: "p", PersonName: "n", Department: "d", Role: "实施工程师", Contact: strings.Repeat("x", 257), SyncedAt: now}}
	for _, value := range append(cases, empty, duplicate, role, contact) {
		if err := validateSnapshot("t", value); err == nil {
			t.Fatalf("accepted %#v", value)
		}
	}
}
func TestHTTPSourceRequestDoesNotSendTenant(t *testing.T) {
	var request *http.Request
	source := &HTTPSource{endpoint: "https://pms.example/api/v1/pms/technicians", nonceReader: strings.NewReader(strings.Repeat("n", 32)), client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		request = r
		return httptest.NewRecorder().Result(), nil
	})}}
	_, _ = source.Fetch(context.Background(), "secret-tenant")
	if strings.Contains(request.URL.String(), "secret-tenant") || request.Header.Get("X-Tenant-ID") != "" {
		t.Fatal("local tenant leaked to shared PMS source")
	}
}
