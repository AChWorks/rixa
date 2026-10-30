// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"time"

	"github.com/AChWorks/achrix"
)

// ContentListPage is a bounded window over the current saved heads, newest first.
// Concurrent edits can move entries; continuation is not a frozen snapshot.
type ContentListPage struct {
	Items      []ContentSummary
	NextCursor string
}

// RevisionPage contains immutable revisions in descending revision order.
type RevisionPage struct {
	Items      []RevisionSummary
	NextCursor string
}

type contentPagePosition struct {
	updated time.Time
	id      string
}

// ListPage returns at most limit entries (default 50, maximum 100). Cursors are
// opaque URL-safe positions, never authority; authorization runs on every page.
func (s *EditorialService) ListPage(ctx context.Context, actor achrix.Principal, cursor string, limit int) (ContentListPage, error) {
	limit, err := editorialPageLimit(limit, 50, 100)
	if err != nil {
		return ContentListPage{}, err
	}
	position, err := decodeContentPageCursor(cursor)
	if err != nil {
		return ContentListPage{}, err
	}
	if err = s.authorize(ctx, actor, CapabilityContentList, ContentCollectionTarget); err != nil {
		return ContentListPage{}, err
	}
	return s.store.listPage(ctx, position, limit)
}

// RevisionsPage returns at most limit revisions (default 25, maximum 50).
// Its cursor is bound to the requested content ID and uses exact int64 values.
func (s *EditorialService) RevisionsPage(ctx context.Context, actor achrix.Principal, id, cursor string, limit int) (RevisionPage, error) {
	if !validEditorialID(id) {
		return RevisionPage{}, ErrEditorialInvalid
	}
	limit, err := editorialPageLimit(limit, 25, 50)
	if err != nil {
		return RevisionPage{}, err
	}
	before, err := decodeRevisionPageCursor(id, cursor)
	if err != nil {
		return RevisionPage{}, err
	}
	if err = s.authorize(ctx, actor, CapabilityContentRead, id); err != nil {
		return RevisionPage{}, err
	}
	return s.store.revisionsPage(ctx, id, before, limit)
}

func editorialPageLimit(limit, fallback, maximum int) (int, error) {
	if limit == 0 {
		return fallback, nil
	}
	if limit < 1 || limit > maximum {
		return 0, ErrEditorialInvalid
	}
	return limit, nil
}

func encodeContentPageCursor(item ContentSummary) string {
	data := make([]byte, 35)
	data[0] = 1
	binary.BigEndian.PutUint64(data[1:9], uint64(item.UpdatedAt.UnixMicro()))
	copy(data[9:], item.ID)
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeContentPageCursor(cursor string) (*contentPagePosition, error) {
	if cursor == "" {
		return nil, nil
	}
	data, err := decodeEditorialPageCursor(cursor, 1)
	if err != nil {
		return nil, err
	}
	micros := int64(binary.BigEndian.Uint64(data[1:9]))
	// Bound the position to finite RFC3339 calendar years before SQL binding.
	if micros < -62135596800000000 || micros > 253402300799999999 {
		return nil, ErrEditorialInvalid
	}
	return &contentPagePosition{updated: time.UnixMicro(micros).UTC(), id: string(data[9:])}, nil
}

func encodeRevisionPageCursor(id string, revision int64) string {
	data := make([]byte, 35)
	data[0] = 2
	binary.BigEndian.PutUint64(data[1:9], uint64(revision))
	copy(data[9:], id)
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeRevisionPageCursor(id, cursor string) (int64, error) {
	if cursor == "" {
		return 0, nil
	}
	data, err := decodeEditorialPageCursor(cursor, 2)
	if err != nil {
		return 0, err
	}
	revision := int64(binary.BigEndian.Uint64(data[1:9]))
	if revision < 1 || string(data[9:]) != id {
		return 0, ErrEditorialInvalid
	}
	return revision, nil
}

func decodeEditorialPageCursor(cursor string, version byte) ([]byte, error) {
	// Exact encoded/decoded size bounds allocation and excludes padding, trailing
	// material, alternate encodings and values intended for another page kind.
	if len(cursor) != 47 {
		return nil, ErrEditorialInvalid
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(cursor)
	if err != nil || len(data) != 35 || data[0] != version || !validEditorialID(string(data[9:])) {
		return nil, ErrEditorialInvalid
	}
	return data, nil
}

func (s *editorialStore) listPage(parent context.Context, position *contentPagePosition, limit int) (ContentListPage, error) {
	ctx, conn, release, err := s.connection(parent)
	if err != nil {
		return ContentListPage{}, err
	}
	defer release()
	var updated any
	var id string
	if position != nil {
		updated, id = position.updated, position.id
	}
	rows, err := conn.Query(ctx, `
		SELECT i.id,i.kind,i.head_revision,r.title,COALESCE(i.publication_intent_revision,0),i.publication_intent_version,r.created_at
		FROM rixa.content_items i
		JOIN rixa.content_revisions r ON r.item_id=i.id AND r.revision=i.head_revision
		WHERE ($1::timestamptz IS NULL OR r.created_at < $1 OR (r.created_at=$1 AND i.id>$2))
		ORDER BY r.created_at DESC,i.id
		LIMIT $3
	`, updated, id, limit+1)
	if err != nil {
		return ContentListPage{}, editorialReadError(err)
	}
	defer rows.Close()
	page := ContentListPage{Items: make([]ContentSummary, 0, limit)}
	for rows.Next() {
		var item ContentSummary
		var kind string
		if err = rows.Scan(&item.ID, &kind, &item.HeadRevision, &item.Title, &item.PublicationIntentRevision, &item.PublicationIntentVersion, &item.UpdatedAt); err != nil {
			return ContentListPage{}, editorialReadError(err)
		}
		if len(page.Items) == limit {
			page.NextCursor = encodeContentPageCursor(page.Items[limit-1])
			continue
		}
		item.Kind = ContentKind(kind)
		item.UpdatedAt = item.UpdatedAt.UTC()
		page.Items = append(page.Items, item)
	}
	if err = rows.Err(); err != nil {
		return ContentListPage{}, editorialReadError(err)
	}
	return page, nil
}

func (s *editorialStore) revisionsPage(parent context.Context, id string, before int64, limit int) (RevisionPage, error) {
	ctx, conn, release, err := s.connection(parent)
	if err != nil {
		return RevisionPage{}, err
	}
	defer release()
	rows, err := conn.Query(ctx, `
		SELECT revision,title,actor,created_at
		FROM rixa.content_revisions
		WHERE item_id=$1 AND ($2::bigint=0 OR revision<$2)
		ORDER BY revision DESC
		LIMIT $3
	`, id, before, limit+1)
	if err != nil {
		return RevisionPage{}, editorialReadError(err)
	}
	page := RevisionPage{Items: make([]RevisionSummary, 0, limit)}
	for rows.Next() {
		var revision RevisionSummary
		if err = rows.Scan(&revision.Revision, &revision.Title, &revision.Actor, &revision.CreatedAt); err != nil {
			rows.Close()
			return RevisionPage{}, editorialReadError(err)
		}
		if len(page.Items) == limit {
			page.NextCursor = encodeRevisionPageCursor(id, page.Items[limit-1].Revision)
			continue
		}
		revision.CreatedAt = revision.CreatedAt.UTC()
		page.Items = append(page.Items, revision)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return RevisionPage{}, editorialReadError(err)
	}
	if len(page.Items) == 0 {
		var exists bool
		if err = conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM rixa.content_items WHERE id=$1)", id).Scan(&exists); err != nil {
			return RevisionPage{}, editorialReadError(err)
		}
		if !exists {
			return RevisionPage{}, ErrEditorialNotFound
		}
	}
	return page, nil
}
