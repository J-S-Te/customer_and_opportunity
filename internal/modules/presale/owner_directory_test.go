package presale

import (
	"context"
	"fmt"
	"testing"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/ownerdirectory"
)

type pagedOwnerDirectoryStub struct {
	queries []ownerdirectory.Query
	users   []ownerdirectory.User
}

func (stub *pagedOwnerDirectoryStub) List(_ context.Context, query ownerdirectory.Query) (ownerdirectory.Page, error) {
	stub.queries = append(stub.queries, query)
	start := (query.Page - 1) * query.PageSize
	if start >= len(stub.users) {
		return ownerdirectory.Page{Page: query.Page, PageSize: query.PageSize, Total: int64(len(stub.users))}, nil
	}
	end := start + query.PageSize
	if end > len(stub.users) {
		end = len(stub.users)
	}
	return ownerdirectory.Page{Items: stub.users[start:end], Page: query.Page, PageSize: query.PageSize, Total: int64(len(stub.users))}, nil
}

func (stub *pagedOwnerDirectoryStub) Validate(context.Context, string, string) error { return nil }
func (stub *pagedOwnerDirectoryStub) Resolve(_ context.Context, userIDs []string) (map[string]ownerdirectory.User, error) {
	wanted := make(map[string]bool, len(userIDs))
	for _, userID := range userIDs {
		wanted[userID] = true
	}
	result := make(map[string]ownerdirectory.User)
	for _, user := range stub.users {
		if wanted[user.ID] {
			result[user.ID] = user
		}
	}
	return result, nil
}

func TestListOwnerDirectoryUsersPaginatesWithinPlatformLimit(t *testing.T) {
	stub := &pagedOwnerDirectoryStub{}
	for index := 0; index < 61; index++ {
		stub.users = append(stub.users, ownerdirectory.User{ID: fmt.Sprintf("user-%d", index)})
	}
	users, err := listOwnerDirectoryUsers(context.Background(), stub)
	if err != nil || len(users) != len(stub.users) {
		t.Fatalf("users=%d error=%v", len(users), err)
	}
	if len(stub.queries) != 2 || stub.queries[0].PageSize != 50 || stub.queries[1].Page != 2 {
		t.Fatalf("queries=%+v", stub.queries)
	}
}

func TestResolveOwnerDisplayNamesUsesPlatformDirectory(t *testing.T) {
	stub := &pagedOwnerDirectoryStub{users: []ownerdirectory.User{{ID: "user-1", DisplayName: "销售张三"}}}
	names, err := resolveOwnerDisplayNames(context.Background(), stub, []string{"user-1"})
	if err != nil || names["user-1"] != "销售张三" {
		t.Fatalf("names=%+v error=%v", names, err)
	}
}
