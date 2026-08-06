package seeddemo

import "testing"

func TestResolveOwnerPrefersRealOverride(t *testing.T) {
	seed := &seeder{actorID: "real-actor", actorName: "真实用户", owners: map[string]Person{
		"张伟": {Sub: "01KYKP456MNNWCBQNCQZCMHYYV", Name: "张伟", OrgID: "01J00000000000000000000003"},
	}}
	owner := seed.resolveOwner("张伟")
	if owner.Sub != "01KYKP456MNNWCBQNCQZCMHYYV" || owner.OrgID == "" {
		t.Fatalf("override not applied: %+v", owner)
	}
}

func TestResolveOwnerFallsBackToActor(t *testing.T) {
	seed := &seeder{actorID: "01KYKP456MNNWCBQNCQZCMHYYV", actorName: "超级管理员"}
	owner := seed.resolveOwner("李娜")
	if owner.Sub != "01KYKP456MNNWCBQNCQZCMHYYV" || owner.Name != "超级管理员" {
		t.Fatalf("actor fallback not applied: %+v", owner)
	}
}
