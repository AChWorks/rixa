// SPDX-License-Identifier: MPL-2.0
package releasepackage

import "testing"

func TestAuthenticatedNativeComponentProfilesRemainExactAndHistorical(t *testing.T) {
	makeProfile := func(paths []string) map[string]string {
		result := make(map[string]string, len(paths))
		for _, p := range paths {
			result[p] = "v1.0.0"
		}
		return result
	}
	published := makeProfile(publishedComponentPaths)
	current := makeProfile(updateComponentPaths)
	if !ValidComponents(published) || !ValidComponents(current) {
		t.Fatal("a known exact historical or current signed package graph was rejected")
	}
	cases := []struct {
		name  string
		mapOf func() map[string]string
	}{
		{"missing-OAuth", func() map[string]string {
			c := makeProfile(updateComponentPaths)
			delete(c, "github.com/AChWorks/achrix/oauth")
			return c
		}},
		{"unknown extra", func() map[string]string {
			c := makeProfile(updateComponentPaths)
			c["github.com/AChWorks/achrix/unknown"] = "v1.0.0"
			return c
		}},
		{"missing-current-identity", func() map[string]string {
			c := makeProfile(updateComponentPaths)
			delete(c, "github.com/AChWorks/achrix/identity")
			return c
		}},
		{"pseudo-OAuth", func() map[string]string {
			c := makeProfile(updateComponentPaths)
			c["github.com/AChWorks/achrix/oauth"] = "v0.0.0-20261101010101-aabbccddeeff"
			return c
		}},
		{"old-with-Bridge", func() map[string]string {
			c := makeProfile(publishedComponentPaths)
			c["github.com/AChWorks/achrix/bridge"] = "v0.1.0"
			return c
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if ValidComponents(tc.mapOf()) {
				t.Fatal("unexpected module graph accepted")
			}
		})
	}
	if len(ComponentPaths()) != len(updateComponentPaths) {
		t.Fatal("new candidate compiled inventory has an incomplete module set")
	}
}
