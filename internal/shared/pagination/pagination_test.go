package pagination

import "testing"

func TestParseQuery(t *testing.T) {
	tests := []struct {
		name      string
		page      string
		pageSize  string
		wantPage  int
		wantSize  int
		wantError bool
	}{
		{name: "defaults", page: "1", pageSize: "20", wantPage: 1, wantSize: 20},
		{name: "maximum", page: "3", pageSize: "100", wantPage: 3, wantSize: 100},
		{name: "invalid page", page: "0", pageSize: "20", wantError: true},
		{name: "invalid page size", page: "1", pageSize: "101", wantError: true},
		{name: "invalid text", page: "x", pageSize: "20", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			page, pageSize, err := ParseQuery(test.page, test.pageSize)
			if (err != nil) != test.wantError {
				t.Fatalf("error=%v, wantError=%v", err, test.wantError)
			}
			if !test.wantError && (page != test.wantPage || pageSize != test.wantSize) {
				t.Fatalf("got page=%d pageSize=%d, want page=%d pageSize=%d", page, pageSize, test.wantPage, test.wantSize)
			}
		})
	}
}
