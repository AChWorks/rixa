// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"errors"
	"os/user"
	"strconv"
	"strings"
)

// useradd --user-group must not adopt or conflict with an unrelated named group.
func unusedRuntimeGroup(lookup func(string) (*user.Group, error)) *Problem {
	_, e := lookup("rixa")
	if e == nil {
		return &Problem{"runtime_group", "ownership", "An existing rixa group is not owned by this operation; inspect it instead of replacing or adopting it."}
	}
	var unknown user.UnknownGroupError
	if errors.As(e, &unknown) {
		return nil
	}
	return &Problem{"runtime_group", "group-unavailable", "The rixa group name cannot be reliably inspected; correct the account/group lookup before applying."}
}
func accountGroupMatches(named *user.Group, accountGID string, recordedGID uint32, memberships []string) bool {
	if named == nil || named.Name != "rixa" || named.Gid != accountGID || len(memberships) == 0 {
		return false
	}
	gid, e := intID(accountGID)
	if e != nil || recordedGID != 0 && recordedGID != gid {
		return false
	}
	// The fixed service must not inherit administrative/shared supplementary
	// privileges after the account or its groups are edited outside this manager.
	for _, group := range memberships {
		n, e := intID(group)
		if e != nil || n != gid {
			return false
		}
	}
	return true
}
func runtimeCredentialsMatch(status []byte, uid, gid uint32) bool {
	seen := map[string]bool{}
	for _, line := range strings.Split(string(status), "\n") {
		key, values, ok := strings.Cut(line, ":")
		if !ok || key != "Uid" && key != "Gid" && key != "Groups" {
			continue
		}
		if seen[key] {
			return false
		}
		seen[key] = true
		fields := strings.Fields(values)
		if key != "Groups" && len(fields) != 4 {
			return false
		}
		expected := gid
		if key == "Uid" {
			expected = uid
		}
		for _, field := range fields {
			n, e := strconv.ParseUint(field, 10, 32)
			if e != nil || uint32(n) != expected {
				return false
			}
		}
	}
	return uid != 0 && gid != 0 && seen["Uid"] && seen["Gid"] && seen["Groups"]
}
