// SPDX-License-Identifier: MPL-2.0

package product

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestEditorialPageCursorsAreBoundedAndExact(t *testing.T) {
	id := "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	item := ContentSummary{ID: id, UpdatedAt: time.Date(2026, 10, 30, 1, 2, 3, 456789000, time.UTC)}
	cursor := encodeContentPageCursor(item)
	position, err := decodeContentPageCursor(cursor)
	if err != nil || position.id != id || !position.updated.Equal(item.UpdatedAt) {
		t.Fatalf("content cursor round trip = %#v, %v", position, err)
	}
	const exactRevision int64 = 9007199254740993
	historyCursor := encodeRevisionPageCursor(id, exactRevision)
	revision, err := decodeRevisionPageCursor(id, historyCursor)
	if err != nil || revision != exactRevision {
		t.Fatalf("revision cursor lost int64 precision: %d, %v", revision, err)
	}

	for _, bad := range []string{"x", strings.Repeat("x", 10000), cursor + "=", cursor + "/", strings.ReplaceAll(cursor, "A", "!"), historyCursor} {
		if _, err := decodeContentPageCursor(bad); !errors.Is(err, ErrEditorialInvalid) {
			t.Errorf("content cursor %q: %v", bad[:min(len(bad), 60)], err)
		}
	}
	for _, bad := range []string{cursor, historyCursor + "=", encodeRevisionPageCursor(id, 0), encodeRevisionPageCursor(id, -1)} {
		if _, err := decodeRevisionPageCursor(id, bad); !errors.Is(err, ErrEditorialInvalid) {
			t.Errorf("revision cursor %q: %v", bad, err)
		}
	}
	if _, err := decodeRevisionPageCursor("ZYXWVUTSRQPONMLKJIHGFEDCBA", historyCursor); !errors.Is(err, ErrEditorialInvalid) {
		t.Fatalf("cursor accepted for another content ID: %v", err)
	}

	data, _ := base64.RawURLEncoding.DecodeString(cursor)
	binary.BigEndian.PutUint64(data[1:9], uint64(253402300800000000))
	if _, err := decodeContentPageCursor(base64.RawURLEncoding.EncodeToString(data)); !errors.Is(err, ErrEditorialInvalid) {
		t.Fatalf("unbounded calendar position accepted: %v", err)
	}
	data[9] = '0'
	if _, err := decodeContentPageCursor(base64.RawURLEncoding.EncodeToString(data)); !errors.Is(err, ErrEditorialInvalid) {
		t.Fatalf("malformed ID accepted: %v", err)
	}
	if position, err := decodeContentPageCursor(""); err != nil || position != nil {
		t.Fatalf("first content page = %#v, %v", position, err)
	}
	if revision, err := decodeRevisionPageCursor(id, ""); err != nil || revision != 0 {
		t.Fatalf("first history page = %d, %v", revision, err)
	}
}

func TestEditorialPageBoundsRejectBeforeStorage(t *testing.T) {
	service := &EditorialService{}
	for _, limit := range []int{-1, 101} {
		if _, err := service.ListPage(t.Context(), "", "", limit); !errors.Is(err, ErrEditorialInvalid) {
			t.Errorf("content limit %d: %v", limit, err)
		}
	}
	for _, limit := range []int{-1, 51} {
		if _, err := service.RevisionsPage(t.Context(), "", "ABCDEFGHIJKLMNOPQRSTUVWXYZ", "", limit); !errors.Is(err, ErrEditorialInvalid) {
			t.Errorf("history limit %d: %v", limit, err)
		}
	}
	if _, err := service.ListPage(t.Context(), "", "malformed", 0); !errors.Is(err, ErrEditorialInvalid) {
		t.Fatalf("invalid content cursor reached authorization/storage: %v", err)
	}
	if _, err := service.RevisionsPage(t.Context(), "", "../item", "", 0); !errors.Is(err, ErrEditorialInvalid) {
		t.Fatalf("invalid content ID reached authorization/storage: %v", err)
	}
	for _, test := range []struct{ limit, fallback, maximum, want int }{{0, 50, 100, 50}, {100, 50, 100, 100}, {0, 25, 50, 25}, {50, 25, 50, 50}} {
		got, err := editorialPageLimit(test.limit, test.fallback, test.maximum)
		if err != nil || got != test.want {
			t.Errorf("page limit %#v = %d, %v", test, got, err)
		}
	}
}
