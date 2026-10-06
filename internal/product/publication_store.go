// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"strconv"

	"github.com/jackc/pgx/v5"
)

const maxPublishedItems = 100

type publicationSnapshot struct {
	Appearance  Appearance
	Contents    []ContentRevision
	Fingerprint string
}

func (s *editorialStore) publicationSnapshot(parent context.Context) (publicationSnapshot, error) {
	ctx, conn, release, err := s.connection(parent)
	if err != nil {
		return publicationSnapshot{}, err
	}
	defer release()

	tx, err := conn.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return publicationSnapshot{}, editorialReadError(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	appearance, err := loadAppearance(ctx, tx)
	if err != nil {
		return publicationSnapshot{}, err
	}

	rows, err := tx.Query(ctx, `
		SELECT id,publication_intent_revision
		FROM rixa.content_items
		WHERE publication_intent_revision IS NOT NULL
		ORDER BY id
		LIMIT $1
	`, maxPublishedItems+1)
	if err != nil {
		return publicationSnapshot{}, editorialReadError(err)
	}
	type selected struct {
		id       string
		revision int64
	}
	selectedRows := make([]selected, 0)
	for rows.Next() {
		var item selected
		if err = rows.Scan(&item.id, &item.revision); err != nil {
			rows.Close()
			return publicationSnapshot{}, editorialReadError(err)
		}
		selectedRows = append(selectedRows, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return publicationSnapshot{}, editorialReadError(err)
	}
	rows.Close()
	if len(selectedRows) > maxPublishedItems {
		return publicationSnapshot{}, ErrEditorialLimited
	}

	result := publicationSnapshot{Appearance: appearance, Contents: make([]ContentRevision, 0, len(selectedRows))}
	for _, selectedItem := range selectedRows {
		revision, loadErr := loadContentRevision(ctx, tx, selectedItem.id, selectedItem.revision)
		if loadErr != nil {
			return publicationSnapshot{}, loadErr
		}
		if revision.PublicationIntentRevision != selectedItem.revision {
			return publicationSnapshot{}, ErrEditorialConflict
		}
		result.Contents = append(result.Contents, revision)
	}

	if err = tx.Commit(ctx); err != nil {
		return publicationSnapshot{}, editorialReadError(err)
	}
	result.Fingerprint = publicationSnapshotFingerprint(result)
	return result, nil
}

func publicationSnapshotFingerprint(snapshot publicationSnapshot) string {
	a := snapshot.Appearance
	parts := []string{
		"rixa.publication.snapshot.v1",
		strconv.FormatInt(a.Revision, 10),
		a.SiteTitle,
		a.SiteDescription,
		a.SiteLanguage,
		a.HomeMode,
		a.HomePageID,
		strconv.FormatBool(a.HeaderShowTitle),
		a.HeaderTagline,
		a.FooterText,
		a.Theme,
	}
	for _, content := range snapshot.Contents {
		parts = append(parts,
			content.ID,
			string(content.Kind),
			strconv.FormatInt(content.Revision, 10),
			content.Title,
			content.BodyHTML,
			content.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999Z07:00"),
			strconv.FormatInt(content.PublicationIntentVersion, 10),
		)
		for _, ref := range content.Media {
			parts = append(parts, ref.AssetID, strconv.FormatInt(ref.AssetRevision, 10))
		}
	}
	return operationRequestHash(parts...)
}
