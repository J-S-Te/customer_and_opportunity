package presale

import "testing"

func TestAlertRecipientPredicateUsesExactUserPersonUnion(t *testing.T) {
	actor := Actor{TenantID: "tenant-a", UserID: "same-id", PersonID: "same-id", ScopeMode: "ALL"}
	if predicate := alertRecipientPredicate(actor); predicate != "((recipient_kind=? AND recipient_id=?) OR (recipient_kind=? AND recipient_id=?))" {
		t.Fatalf("predicate=%q", predicate)
	}
	args := alertRecipientArgsWithoutTenant(actor)
	want := []any{AlertRecipientUser, "same-id", AlertRecipientPerson, "same-id"}
	if len(args) != len(want) {
		t.Fatalf("args=%v", args)
	}
	for index := range want {
		if args[index] != want[index] {
			t.Fatalf("args=%v", args)
		}
	}
}

func TestAlertRecipientPredicateDoesNotInferMissingPerson(t *testing.T) {
	actor := Actor{TenantID: "tenant-a", UserID: "user-a", ScopeMode: "ALL"}
	if predicate := alertRecipientPredicate(actor); predicate != "(recipient_kind=? AND recipient_id=?)" {
		t.Fatalf("predicate=%q", predicate)
	}
	args := alertRecipientArgsWithoutTenant(actor)
	if len(args) != 2 || args[0] != AlertRecipientUser || args[1] != actor.UserID {
		t.Fatalf("args=%v", args)
	}
}

func TestAlertRecipientPredicateTreatsWhitespacePersonBindingAsMissing(t *testing.T) {
	t.Parallel()
	actor := Actor{TenantID: "tenant-a", UserID: "user-a", PersonID: " \t", ScopeMode: "ALL"}
	if predicate := alertRecipientPredicate(actor); predicate != "(recipient_kind=? AND recipient_id=?)" {
		t.Fatalf("predicate=%q", predicate)
	}
	args := alertRecipientArgsWithoutTenant(actor)
	if len(args) != 2 || args[0] != AlertRecipientUser || args[1] != actor.UserID {
		t.Fatalf("args=%v", args)
	}
}
