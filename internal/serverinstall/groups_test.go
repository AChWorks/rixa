// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"errors"
	"os/user"
	"strings"
	"testing"
)

func TestExistingRuntimeGroupBlocksBeforeAccountCreation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		group *user.Group
		err   error
		want  string
	}{
		{"unused", nil, user.UnknownGroupError("rixa"), ""},
		{"unrelated-existing", &user.Group{Name: "rixa", Gid: "2000"}, nil, "ownership"},
		{"lookup-failed", nil, errors.New("group provider unavailable"), "group-unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			problem := unusedRuntimeGroup(func(name string) (*user.Group, error) {
				if name != "rixa" {
					t.Fatal("unexpected group name")
				}
				return tc.group, tc.err
			})
			if tc.want == "" {
				if problem != nil {
					t.Fatal("known unused group rejected")
				}
				return
			}
			if problem == nil || problem.Field != "runtime_group" || problem.Code != tc.want || problem.Message == "" {
				t.Fatalf("missing actionable group admission: %+v", problem)
			}
		})
	}
}
func TestNamedRuntimeGroupGIDAndMembershipBinding(t *testing.T) {
	for _, tc := range []struct {
		name     string
		named    *user.Group
		account  string
		recorded uint32
		groups   []string
		want     bool
	}{
		{"fresh-owned", &user.Group{Name: "rixa", Gid: "321"}, "321", 0, []string{"321"}, true},
		{"retained-owned", &user.Group{Name: "rixa", Gid: "321"}, "321", 321, []string{"321"}, true},
		{"named-group-drift", &user.Group{Name: "rixa", Gid: "999"}, "321", 321, []string{"321"}, false},
		{"recorded-gid-drift", &user.Group{Name: "rixa", Gid: "321"}, "321", 322, []string{"321"}, false},
		{"wrong-name", &user.Group{Name: "shared", Gid: "321"}, "321", 321, []string{"321"}, false},
		{"root-group", &user.Group{Name: "rixa", Gid: "0"}, "0", 0, []string{"0"}, false},
		{"extra-privilege", &user.Group{Name: "rixa", Gid: "321"}, "321", 321, []string{"321", "27"}, false},
		{"unproved-membership", &user.Group{Name: "rixa", Gid: "321"}, "321", 321, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := accountGroupMatches(tc.named, tc.account, tc.recorded, tc.groups); got != tc.want {
				t.Fatalf("group ownership=%t want=%t", got, tc.want)
			}
		})
	}
}
func TestRuntimeUIDGIDAndSupplementaryPrivilege(t *testing.T) {
	valid := "Name:\trixa\nUid:\t321\t321\t321\t321\nGid:\t654\t654\t654\t654\nGroups:\t654\n"
	for _, tc := range []struct {
		name, status string
		want         bool
	}{
		{"owned", valid, true},
		{"no-extra-groups", strings.Replace(valid, "Groups:\t654", "Groups:", 1), true},
		{"primary-gid-drift", strings.Replace(valid, "Gid:\t654\t654\t654\t654", "Gid:\t655\t655\t655\t655", 1), false},
		{"effective-gid-drift", strings.Replace(valid, "Gid:\t654\t654\t654\t654", "Gid:\t654\t0\t654\t654", 1), false},
		{"uid-drift", strings.Replace(valid, "Uid:\t321\t321\t321\t321", "Uid:\t321\t0\t321\t321", 1), false},
		{"privileged-supplementary", strings.Replace(valid, "Groups:\t654", "Groups:\t654 27", 1), false},
		{"missing-groups", strings.Replace(valid, "Groups:\t654\n", "", 1), false},
		{"ambiguous-gid", valid + "Gid:\t0\t0\t0\t0\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := runtimeCredentialsMatch([]byte(tc.status), 321, 654); got != tc.want {
				t.Fatalf("runtime credentials=%t want=%t", got, tc.want)
			}
		})
	}
}
