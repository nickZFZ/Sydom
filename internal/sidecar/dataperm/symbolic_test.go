package dataperm

import (
	"testing"

	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
)

// stubResolver 满足 RoleResolver，返回固定隐式角色集。
type stubResolver struct{ roles []string }

func (s stubResolver) GetImplicitRolesForUser(_, _ string) ([]string, error) {
	return s.roles, nil
}

var sdpID uint64

func sdp(subjType, subjID, resource, effect, cond string) kernel.DataPolicy {
	sdpID++
	return kernel.DataPolicy{ID: sdpID, SubjectType: subjType, SubjectID: subjID, Resource: resource, Condition: cond, Effect: effect}
}

func newFilterWith(roles []string, policies ...kernel.DataPolicy) *Filter {
	t := NewTable()
	t.ApplySnapshot(policies)
	return NewFilter(stubResolver{roles: roles}, t)
}

func TestFilterSymbolic_ConditionalUserVarPreserved(t *testing.T) {
	f := newFilterWith(
		[]string{"sales"},
		sdp("user", "alice", "orders", "allow", `{"op":"EQ","field":"region","value":"$user.region"}`),
	)
	sr, err := f.FilterSymbolic("alice", "1", "orders")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if sr.Match != MatchConditional {
		t.Fatalf("match=%q want conditional", sr.Match)
	}
	if sr.Predicate != "region = $user.region" {
		t.Fatalf("predicate=%q", sr.Predicate)
	}
}

func TestFilterSymbolic_DenyOverrideShape(t *testing.T) {
	f := newFilterWith(
		[]string{"sales"},
		sdp("role", "sales", "orders", "allow", `{"op":"EQ","field":"region","value":"east"}`),
		sdp("user", "alice", "orders", "deny", `{"op":"EQ","field":"status","value":"archived"}`),
	)
	sr, err := f.FilterSymbolic("alice", "1", "orders")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if sr.Match != MatchConditional || sr.Predicate != "(region = 'east' AND NOT (status = 'archived'))" {
		t.Fatalf("got match=%q predicate=%q", sr.Match, sr.Predicate)
	}
}

func TestFilterSymbolic_AllWhenUnconfigured(t *testing.T) {
	f := newFilterWith(nil)
	sr, _ := f.FilterSymbolic("alice", "1", "orders")
	if sr.Match != MatchAll {
		t.Fatalf("match=%q want all", sr.Match)
	}
}

func TestFilterSymbolic_NoneWhenNoAllowHit(t *testing.T) {
	f := newFilterWith(
		nil, // alice 无角色
		sdp("role", "sales", "orders", "allow", `{"op":"EQ","field":"region","value":"east"}`),
	)
	sr, _ := f.FilterSymbolic("alice", "1", "orders")
	if sr.Match != MatchNone {
		t.Fatalf("match=%q want none", sr.Match)
	}
}
