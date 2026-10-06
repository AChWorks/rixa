// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/AChWorks/achrix"
	"github.com/AChWorks/achrix/media"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	ContentCollectionTarget   = "collection"
	AppearanceTarget          = "appearance"
	OperationCollectionTarget = "operations"
	editorialDBTimeout        = 5 * time.Second
)

type ContentSummary struct {
	ID                        string
	Kind                      ContentKind
	HeadRevision              int64
	Title                     string
	PublicationIntentRevision int64
	PublicationIntentVersion  int64
	UpdatedAt                 time.Time
}

type RevisionSummary struct {
	Revision  int64
	Title     string
	Actor     string
	CreatedAt time.Time
}

type ContentMediaRef struct {
	AssetID       string
	AssetRevision int64
}

type ContentRevision struct {
	ID                        string
	Kind                      ContentKind
	Revision                  int64
	Title                     string
	BodyHTML                  string
	Actor                     string
	CreatedAt                 time.Time
	PublicationIntentRevision int64
	PublicationIntentVersion  int64
	Media                     []ContentMediaRef
}

type EditorialOperation struct {
	ID             string
	Kind           string
	ResourceID     string
	RequestHash    string
	ResultRevision int64
	CreatedAt      time.Time
}

type Appearance struct {
	Revision        int64
	SiteTitle       string
	SiteDescription string
	SiteLanguage    string
	HomeMode        string
	HomePageID      string
	HeaderShowTitle bool
	HeaderTagline   string
	FooterText      string
	Theme           string
	Actor           string
	CreatedAt       time.Time
}

func (a Appearance) input() AppearanceInput {
	return AppearanceInput{
		SiteTitle:       a.SiteTitle,
		SiteDescription: a.SiteDescription,
		SiteLanguage:    a.SiteLanguage,
		HomeMode:        a.HomeMode,
		HomePageID:      a.HomePageID,
		HeaderShowTitle: a.HeaderShowTitle,
		HeaderTagline:   a.HeaderTagline,
		FooterText:      a.FooterText,
		Theme:           a.Theme,
	}
}

type editorialStore struct {
	dsn   string
	slots chan struct{}
}

func newEditorialStore(dsn string) (*editorialStore, error) {
	return newEditorialStoreWithLimit(dsn, defaultEditorialMaxOperations)
}

func newEditorialStoreWithLimit(dsn string, maxOperations int) (*editorialStore, error) {
	if dsn == "" || maxOperations < 1 || maxOperations > maxConfiguredResourceOperations {
		return nil, ErrConfiguration
	}
	return &editorialStore{dsn: dsn, slots: make(chan struct{}, maxOperations)}, nil
}

func (s *editorialStore) connection(parent context.Context) (context.Context, *pgx.Conn, func(), error) {
	if s == nil || s.dsn == "" {
		return nil, nil, nil, ErrEditorialUnavailable
	}
	select {
	case s.slots <- struct{}{}:
	case <-parent.Done():
		return nil, nil, nil, parent.Err()
	default:
		return nil, nil, nil, ErrEditorialLimited
	}
	ctx, cancel := context.WithTimeout(parent, editorialDBTimeout)
	conn, err := pgx.Connect(ctx, s.dsn)
	if err != nil {
		cancel()
		<-s.slots
		return nil, nil, nil, editorialReadError(err)
	}
	release := func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
		_ = conn.Close(closeCtx)
		closeCancel()
		cancel()
		<-s.slots
	}
	return ctx, conn, release, nil
}

func (s *editorialStore) ready(parent context.Context) error {
	ctx, conn, release, err := s.connection(parent)
	if err != nil {
		return err
	}
	defer release()
	plan := editorialMigrationPlan()
	version, err := editorialLedgerVersion(ctx, conn, plan)
	if err != nil || version != len(plan) {
		return ErrEditorialUnavailable
	}
	var head int64
	if err = conn.QueryRow(ctx, "SELECT head_revision FROM rixa.appearance_head WHERE singleton=true").Scan(&head); err != nil {
		return editorialReadError(err)
	}
	if head < 1 {
		return ErrEditorialUnavailable
	}
	return nil
}

type EditorialService struct {
	app             *achrix.Application
	store           *editorialStore
	media           *media.Service
	publicationGate chan struct{}
}

func newEditorialService(app *achrix.Application, mediaService *media.Service, dsn string) (*EditorialService, error) {
	return newEditorialServiceWithLimit(app, mediaService, dsn, defaultEditorialMaxOperations)
}

func newEditorialServiceWithLimit(app *achrix.Application, mediaService *media.Service, dsn string, maxOperations int) (*EditorialService, error) {
	if app == nil || mediaService == nil {
		return nil, ErrConfiguration
	}
	store, err := newEditorialStoreWithLimit(dsn, maxOperations)
	if err != nil {
		return nil, err
	}
	return &EditorialService{
		app: app, store: store, media: mediaService,
		publicationGate: make(chan struct{}, 1),
	}, nil
}

func (s *EditorialService) Ready(ctx context.Context) error {
	if s == nil || s.store == nil {
		return ErrEditorialUnavailable
	}
	return s.store.ready(ctx)
}

func (s *EditorialService) List(ctx context.Context, actor achrix.Principal) ([]ContentSummary, error) {
	if err := s.authorize(ctx, actor, CapabilityContentList, ContentCollectionTarget); err != nil {
		return nil, err
	}
	return s.store.list(ctx)
}

func (s *EditorialService) Head(ctx context.Context, actor achrix.Principal, id string) (ContentRevision, error) {
	if !validEditorialID(id) {
		return ContentRevision{}, ErrEditorialInvalid
	}
	if err := s.authorize(ctx, actor, CapabilityContentRead, id); err != nil {
		return ContentRevision{}, err
	}
	return s.store.head(ctx, id)
}

func (s *EditorialService) Revision(ctx context.Context, actor achrix.Principal, id string, revision int64) (ContentRevision, error) {
	if !validEditorialID(id) || revision < 1 {
		return ContentRevision{}, ErrEditorialInvalid
	}
	if err := s.authorize(ctx, actor, CapabilityContentRead, id); err != nil {
		return ContentRevision{}, err
	}
	return s.store.revision(ctx, id, revision)
}

func (s *EditorialService) Preview(ctx context.Context, actor achrix.Principal, id string, revision int64) (ContentRevision, error) {
	if !validEditorialID(id) || revision < 1 {
		return ContentRevision{}, ErrEditorialInvalid
	}
	if err := s.authorize(ctx, actor, CapabilityContentPreview, id); err != nil {
		return ContentRevision{}, err
	}
	return s.store.revision(ctx, id, revision)
}

func (s *EditorialService) Revisions(ctx context.Context, actor achrix.Principal, id string) ([]RevisionSummary, error) {
	if !validEditorialID(id) {
		return nil, ErrEditorialInvalid
	}
	if err := s.authorize(ctx, actor, CapabilityContentRead, id); err != nil {
		return nil, err
	}
	return s.store.revisions(ctx, id)
}

func (s *EditorialService) Create(ctx context.Context, actor achrix.Principal, operationID string, kind ContentKind, title, body string) (ContentRevision, error) {
	if !validOperationID(operationID) || !validContentKind(kind) || !validContentTitle(title) {
		return ContentRevision{}, ErrEditorialInvalid
	}
	if err := s.authorize(ctx, actor, CapabilityContentEdit, ContentCollectionTarget); err != nil {
		return ContentRevision{}, err
	}
	canonical, ids, err := canonicalBody(body)
	if err != nil {
		return ContentRevision{}, err
	}
	refs, err := s.mediaRefs(ctx, actor, ids)
	if err != nil {
		return ContentRevision{}, err
	}
	return s.store.create(ctx, string(actor), operationID, kind, title, canonical, refs)
}

func (s *EditorialService) Save(ctx context.Context, actor achrix.Principal, operationID, id string, expectedHead int64, title, body string) (ContentRevision, error) {
	if !validOperationID(operationID) || !validEditorialID(id) || expectedHead < 1 || !validContentTitle(title) {
		return ContentRevision{}, ErrEditorialInvalid
	}
	if err := s.authorize(ctx, actor, CapabilityContentEdit, id); err != nil {
		return ContentRevision{}, err
	}
	canonical, ids, err := canonicalBody(body)
	if err != nil {
		return ContentRevision{}, err
	}
	refs, err := s.mediaRefs(ctx, actor, ids)
	if err != nil {
		return ContentRevision{}, err
	}
	return s.store.save(ctx, string(actor), operationID, id, expectedHead, title, canonical, refs)
}

func (s *EditorialService) SetPublicationIntent(ctx context.Context, actor achrix.Principal, operationID, id string, expectedHead, expectedPublicationVersion, revision int64) (ContentRevision, error) {
	if !validOperationID(operationID) || !validEditorialID(id) || expectedHead < 1 || expectedPublicationVersion < 1 || revision < 1 {
		return ContentRevision{}, ErrEditorialInvalid
	}
	if err := s.authorize(ctx, actor, CapabilityContentPublishIntent, id); err != nil {
		return ContentRevision{}, err
	}
	release, err := s.acquirePublicationSource(ctx)
	if err != nil {
		return ContentRevision{}, err
	}
	defer release()
	return s.store.publicationIntent(ctx, string(actor), operationID, id, expectedHead, expectedPublicationVersion, revision, false)
}

func (s *EditorialService) ClearPublicationIntent(ctx context.Context, actor achrix.Principal, operationID, id string, expectedHead, expectedPublicationVersion int64) (ContentRevision, error) {
	if !validOperationID(operationID) || !validEditorialID(id) || expectedHead < 1 || expectedPublicationVersion < 1 {
		return ContentRevision{}, ErrEditorialInvalid
	}
	if err := s.authorize(ctx, actor, CapabilityContentPublishIntent, id); err != nil {
		return ContentRevision{}, err
	}
	release, err := s.acquirePublicationSource(ctx)
	if err != nil {
		return ContentRevision{}, err
	}
	defer release()
	return s.store.publicationIntent(ctx, string(actor), operationID, id, expectedHead, expectedPublicationVersion, 0, true)
}

func (s *EditorialService) Operation(ctx context.Context, actor achrix.Principal, operationID string) (EditorialOperation, error) {
	if !validOperationID(operationID) && operationID != "bootstrap:appearance" {
		return EditorialOperation{}, ErrEditorialInvalid
	}
	if err := s.authorize(ctx, actor, CapabilityOperationRead, OperationCollectionTarget); err != nil {
		return EditorialOperation{}, err
	}
	return s.store.operation(ctx, operationID)
}

func (s *EditorialService) Appearance(ctx context.Context, actor achrix.Principal) (Appearance, error) {
	if err := s.authorize(ctx, actor, CapabilityAppearanceRead, AppearanceTarget); err != nil {
		return Appearance{}, err
	}
	return s.store.appearance(ctx)
}

func (s *EditorialService) SaveAppearance(ctx context.Context, actor achrix.Principal, operationID string, expectedHead int64, input AppearanceInput) (Appearance, error) {
	if !validOperationID(operationID) || expectedHead < 1 {
		return Appearance{}, ErrEditorialInvalid
	}
	if err := input.validateShape(); err != nil {
		return Appearance{}, err
	}
	if err := s.authorize(ctx, actor, CapabilityAppearanceEdit, AppearanceTarget); err != nil {
		return Appearance{}, err
	}
	release, err := s.acquirePublicationSource(ctx)
	if err != nil {
		return Appearance{}, err
	}
	defer release()
	return s.store.saveAppearance(ctx, string(actor), operationID, expectedHead, input)
}

func (s *EditorialService) acquirePublicationSource(ctx context.Context) (func(), error) {
	if s == nil || s.publicationGate == nil {
		return nil, ErrEditorialUnavailable
	}
	select {
	case s.publicationGate <- struct{}{}:
		return func() { <-s.publicationGate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *EditorialService) authorize(ctx context.Context, actor achrix.Principal, capability, resource string) error {
	if s == nil || s.app == nil {
		return ErrEditorialUnavailable
	}
	authCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return s.app.Authorize(authCtx, actor, capability, resource)
}

func (s *EditorialService) mediaRefs(ctx context.Context, actor achrix.Principal, ids []string) ([]ContentMediaRef, error) {
	refs := make([]ContentMediaRef, 0, len(ids))
	for _, id := range ids {
		asset, err := s.media.Status(ctx, actor, id)
		if err != nil {
			if errors.Is(err, media.ErrInput) || errors.Is(err, media.ErrNotFound) || errors.Is(err, media.ErrConflict) {
				return nil, ErrEditorialInvalid
			}
			return nil, err
		}
		if asset.State != "ready" || asset.Revision < 1 {
			return nil, ErrEditorialInvalid
		}
		refs = append(refs, ContentMediaRef{AssetID: asset.ID, AssetRevision: asset.Revision})
	}
	return refs, nil
}

func (s *editorialStore) list(parent context.Context) ([]ContentSummary, error) {
	ctx, conn, release, err := s.connection(parent)
	if err != nil {
		return nil, err
	}
	defer release()
	rows, err := conn.Query(ctx, `
		SELECT i.id,i.kind,i.head_revision,r.title,COALESCE(i.publication_intent_revision,0),i.publication_intent_version,r.created_at
		FROM rixa.content_items i
		JOIN rixa.content_revisions r ON r.item_id=i.id AND r.revision=i.head_revision
		ORDER BY r.created_at DESC,i.id
		LIMIT 100
	`)
	if err != nil {
		return nil, editorialReadError(err)
	}
	defer rows.Close()
	result := make([]ContentSummary, 0)
	for rows.Next() {
		var item ContentSummary
		var kind string
		if err = rows.Scan(&item.ID, &kind, &item.HeadRevision, &item.Title, &item.PublicationIntentRevision, &item.PublicationIntentVersion, &item.UpdatedAt); err != nil {
			return nil, editorialReadError(err)
		}
		item.Kind = ContentKind(kind)
		item.UpdatedAt = item.UpdatedAt.UTC()
		result = append(result, item)
	}
	if err = rows.Err(); err != nil {
		return nil, editorialReadError(err)
	}
	return result, nil
}

func (s *editorialStore) head(parent context.Context, id string) (ContentRevision, error) {
	ctx, conn, release, err := s.connection(parent)
	if err != nil {
		return ContentRevision{}, err
	}
	defer release()
	var revision int64
	if err = conn.QueryRow(ctx, "SELECT head_revision FROM rixa.content_items WHERE id=$1", id).Scan(&revision); err != nil {
		return ContentRevision{}, editorialReadError(err)
	}
	return loadContentRevision(ctx, conn, id, revision)
}

func (s *editorialStore) revision(parent context.Context, id string, revision int64) (ContentRevision, error) {
	ctx, conn, release, err := s.connection(parent)
	if err != nil {
		return ContentRevision{}, err
	}
	defer release()
	return loadContentRevision(ctx, conn, id, revision)
}

func loadContentRevision(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, id string, revision int64) (ContentRevision, error) {
	var value ContentRevision
	var kind string
	if err := q.QueryRow(ctx, `
		SELECT i.id,i.kind,r.revision,r.title,r.body_html,r.actor,r.created_at,
		       COALESCE(i.publication_intent_revision,0),i.publication_intent_version
		FROM rixa.content_items i
		JOIN rixa.content_revisions r ON r.item_id=i.id
		WHERE i.id=$1 AND r.revision=$2
	`, id, revision).Scan(
		&value.ID, &kind, &value.Revision, &value.Title, &value.BodyHTML,
		&value.Actor, &value.CreatedAt, &value.PublicationIntentRevision, &value.PublicationIntentVersion,
	); err != nil {
		return ContentRevision{}, editorialReadError(err)
	}
	value.Kind = ContentKind(kind)
	value.CreatedAt = value.CreatedAt.UTC()
	rows, err := q.Query(ctx, `
		SELECT asset_id,asset_revision
		FROM rixa.content_media_refs
		WHERE item_id=$1 AND revision=$2
		ORDER BY asset_id
	`, id, revision)
	if err != nil {
		return ContentRevision{}, editorialReadError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var ref ContentMediaRef
		if err = rows.Scan(&ref.AssetID, &ref.AssetRevision); err != nil {
			return ContentRevision{}, editorialReadError(err)
		}
		value.Media = append(value.Media, ref)
	}
	if err = rows.Err(); err != nil {
		return ContentRevision{}, editorialReadError(err)
	}
	return value, nil
}

func (s *editorialStore) revisions(parent context.Context, id string) ([]RevisionSummary, error) {
	ctx, conn, release, err := s.connection(parent)
	if err != nil {
		return nil, err
	}
	defer release()
	rows, err := conn.Query(ctx, `
		SELECT revision,title,actor,created_at
		FROM rixa.content_revisions
		WHERE item_id=$1
		ORDER BY revision DESC
		LIMIT 50
	`, id)
	if err != nil {
		return nil, editorialReadError(err)
	}
	defer rows.Close()
	result := make([]RevisionSummary, 0)
	for rows.Next() {
		var revision RevisionSummary
		if err = rows.Scan(&revision.Revision, &revision.Title, &revision.Actor, &revision.CreatedAt); err != nil {
			return nil, editorialReadError(err)
		}
		revision.CreatedAt = revision.CreatedAt.UTC()
		result = append(result, revision)
	}
	if err = rows.Err(); err != nil {
		return nil, editorialReadError(err)
	}
	if len(result) == 0 {
		return nil, ErrEditorialNotFound
	}
	return result, nil
}

func (s *editorialStore) create(parent context.Context, actor, operationID string, kind ContentKind, title, body string, refs []ContentMediaRef) (ContentRevision, error) {
	ctx, conn, release, err := s.connection(parent)
	if err != nil {
		return ContentRevision{}, err
	}
	defer release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return ContentRevision{}, editorialMutationKnownError(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	requestHash := contentCreateRequestHash(actor, kind, title, body, refs)
	if err = lockOperation(ctx, tx, operationID); err != nil {
		return ContentRevision{}, editorialMutationKnownError(err)
	}
	if op, found, e := operationIn(ctx, tx, operationID); e != nil {
		return ContentRevision{}, e
	} else if found {
		if op.Kind != "content.create" || op.RequestHash != requestHash {
			return ContentRevision{}, ErrEditorialConflict
		}
		return loadContentRevision(ctx, tx, op.ResourceID, op.ResultRevision)
	}

	id := randText()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err = tx.Exec(ctx, `
		INSERT INTO rixa.content_items(id,kind,head_revision,publication_intent_revision,created_by,created_at)
		VALUES($1,$2,1,NULL,$3,$4)
	`, id, string(kind), actor, now); err != nil {
		return ContentRevision{}, editorialMutationKnownError(err)
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO rixa.content_revisions(item_id,revision,title,body_html,actor,created_at,operation_id)
		VALUES($1,1,$2,$3,$4,$5,$6)
	`, id, title, body, actor, now, operationID); err != nil {
		return ContentRevision{}, editorialMutationKnownError(err)
	}
	if err = insertMediaRefs(ctx, tx, id, 1, refs); err != nil {
		return ContentRevision{}, err
	}
	if err = insertOperation(ctx, tx, EditorialOperation{
		ID: operationID, Kind: "content.create", ResourceID: id, RequestHash: requestHash, ResultRevision: 1, CreatedAt: now,
	}); err != nil {
		return ContentRevision{}, err
	}
	result, err := loadContentRevision(ctx, tx, id, 1)
	if err != nil {
		return ContentRevision{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ContentRevision{}, editorialCommitError(err)
	}
	return result, nil
}

func (s *editorialStore) save(parent context.Context, actor, operationID, id string, expectedHead int64, title, body string, refs []ContentMediaRef) (ContentRevision, error) {
	ctx, conn, release, err := s.connection(parent)
	if err != nil {
		return ContentRevision{}, err
	}
	defer release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return ContentRevision{}, editorialMutationKnownError(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	requestHash := contentRequestHash("content.save", actor, id, expectedHead, title, body, refs)

	if err = lockOperation(ctx, tx, operationID); err != nil {
		return ContentRevision{}, editorialMutationKnownError(err)
	}
	if op, found, e := operationIn(ctx, tx, operationID); e != nil {
		return ContentRevision{}, e
	} else if found {
		if op.Kind != "content.save" || op.ResourceID != id || op.RequestHash != requestHash {
			return ContentRevision{}, ErrEditorialConflict
		}
		return loadContentRevision(ctx, tx, id, op.ResultRevision)
	}

	var current int64
	if err = tx.QueryRow(ctx, "SELECT head_revision FROM rixa.content_items WHERE id=$1 FOR UPDATE", id).Scan(&current); err != nil {
		return ContentRevision{}, editorialMutationKnownError(err)
	}
	if current != expectedHead {
		return ContentRevision{}, ErrEditorialConflict
	}
	next := current + 1
	if next < 1 {
		return ContentRevision{}, ErrEditorialConflict
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err = tx.Exec(ctx, `
		INSERT INTO rixa.content_revisions(item_id,revision,title,body_html,actor,created_at,operation_id)
		VALUES($1,$2,$3,$4,$5,$6,$7)
	`, id, next, title, body, actor, now, operationID); err != nil {
		return ContentRevision{}, editorialMutationKnownError(err)
	}
	if err = insertMediaRefs(ctx, tx, id, next, refs); err != nil {
		return ContentRevision{}, err
	}
	tag, err := tx.Exec(ctx, "UPDATE rixa.content_items SET head_revision=$2 WHERE id=$1 AND head_revision=$3", id, next, current)
	if err != nil {
		return ContentRevision{}, editorialMutationKnownError(err)
	}
	if tag.RowsAffected() != 1 {
		return ContentRevision{}, ErrEditorialConflict
	}
	if err = insertOperation(ctx, tx, EditorialOperation{
		ID: operationID, Kind: "content.save", ResourceID: id, RequestHash: requestHash, ResultRevision: next, CreatedAt: now,
	}); err != nil {
		return ContentRevision{}, err
	}
	result, err := loadContentRevision(ctx, tx, id, next)
	if err != nil {
		return ContentRevision{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ContentRevision{}, editorialCommitError(err)
	}
	return result, nil
}

func (s *editorialStore) publicationIntent(parent context.Context, actor, operationID, id string, expectedHead, expectedPublicationVersion, revision int64, clear bool) (ContentRevision, error) {
	ctx, conn, release, err := s.connection(parent)
	if err != nil {
		return ContentRevision{}, err
	}
	defer release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return ContentRevision{}, editorialMutationKnownError(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if err = lockOperation(ctx, tx, operationID); err != nil {
		return ContentRevision{}, editorialMutationKnownError(err)
	}
	kind := "content.publication-intent.set"
	if clear {
		kind = "content.publication-intent.clear"
	}
	requestHash := operationRequestHash(
		kind,
		actor,
		id,
		strconv.FormatInt(expectedHead, 10),
		strconv.FormatInt(expectedPublicationVersion, 10),
		strconv.FormatInt(revision, 10),
	)
	if op, found, e := operationIn(ctx, tx, operationID); e != nil {
		return ContentRevision{}, e
	} else if found {
		if op.Kind != kind || op.ResourceID != id || op.RequestHash != requestHash {
			return ContentRevision{}, ErrEditorialConflict
		}
		return loadContentRevision(ctx, tx, id, expectedHead)
	}

	var currentHead, currentPublicationVersion int64
	if err = tx.QueryRow(ctx, "SELECT head_revision,publication_intent_version FROM rixa.content_items WHERE id=$1 FOR UPDATE", id).Scan(&currentHead, &currentPublicationVersion); err != nil {
		return ContentRevision{}, editorialMutationKnownError(err)
	}
	if currentHead != expectedHead || currentPublicationVersion != expectedPublicationVersion {
		return ContentRevision{}, ErrEditorialConflict
	}

	resultRevision := int64(0)
	var tag pgconn.CommandTag
	if !clear {
		var exists int
		if err = tx.QueryRow(ctx, "SELECT 1 FROM rixa.content_revisions WHERE item_id=$1 AND revision=$2", id, revision).Scan(&exists); err != nil {
			return ContentRevision{}, editorialMutationKnownError(err)
		}
		tag, err = tx.Exec(ctx, `UPDATE rixa.content_items
			SET publication_intent_revision=$2,publication_intent_version=publication_intent_version+1
			WHERE id=$1 AND head_revision=$3 AND publication_intent_version=$4`, id, revision, expectedHead, expectedPublicationVersion)
		resultRevision = revision
	} else {
		tag, err = tx.Exec(ctx, `UPDATE rixa.content_items
			SET publication_intent_revision=NULL,publication_intent_version=publication_intent_version+1
			WHERE id=$1 AND head_revision=$2 AND publication_intent_version=$3`, id, expectedHead, expectedPublicationVersion)
	}
	if err != nil {
		return ContentRevision{}, editorialMutationKnownError(err)
	}
	if tag.RowsAffected() != 1 {
		return ContentRevision{}, ErrEditorialConflict
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	if err = insertOperation(ctx, tx, EditorialOperation{
		ID: operationID, Kind: kind, ResourceID: id, RequestHash: requestHash, ResultRevision: resultRevision, CreatedAt: now,
	}); err != nil {
		return ContentRevision{}, err
	}
	result, err := loadContentRevision(ctx, tx, id, currentHead)
	if err != nil {
		return ContentRevision{}, err
	}
	if result.PublicationIntentVersion != expectedPublicationVersion+1 {
		return ContentRevision{}, ErrEditorialConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return ContentRevision{}, editorialCommitError(err)
	}
	return result, nil
}

func (s *editorialStore) operation(parent context.Context, operationID string) (EditorialOperation, error) {
	ctx, conn, release, err := s.connection(parent)
	if err != nil {
		return EditorialOperation{}, err
	}
	defer release()
	op, found, err := operationIn(ctx, conn, operationID)
	if err != nil {
		return EditorialOperation{}, err
	}
	if !found {
		return EditorialOperation{}, ErrEditorialNotFound
	}
	return op, nil
}

func (s *editorialStore) appearance(parent context.Context) (Appearance, error) {
	ctx, conn, release, err := s.connection(parent)
	if err != nil {
		return Appearance{}, err
	}
	defer release()
	return loadAppearance(ctx, conn)
}

func loadAppearance(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (Appearance, error) {
	var value Appearance
	if err := q.QueryRow(ctx, `
		SELECT r.revision,r.site_title,r.site_description,r.site_language,r.home_mode,
		       COALESCE(r.home_page_id,''),r.header_show_title,r.header_tagline,r.footer_text,
		       r.theme,r.actor,r.created_at
		FROM rixa.appearance_head h
		JOIN rixa.appearance_revisions r ON r.revision=h.head_revision
		WHERE h.singleton=true
	`).Scan(
		&value.Revision, &value.SiteTitle, &value.SiteDescription, &value.SiteLanguage,
		&value.HomeMode, &value.HomePageID, &value.HeaderShowTitle, &value.HeaderTagline,
		&value.FooterText, &value.Theme, &value.Actor, &value.CreatedAt,
	); err != nil {
		return Appearance{}, editorialReadError(err)
	}
	value.CreatedAt = value.CreatedAt.UTC()
	return value, nil
}

func loadAppearanceRevision(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, revision int64) (Appearance, error) {
	var value Appearance
	if err := q.QueryRow(ctx, `
		SELECT revision,site_title,site_description,site_language,home_mode,
		       COALESCE(home_page_id,''),header_show_title,header_tagline,footer_text,
		       theme,actor,created_at
		FROM rixa.appearance_revisions
		WHERE revision=$1
	`, revision).Scan(
		&value.Revision, &value.SiteTitle, &value.SiteDescription, &value.SiteLanguage,
		&value.HomeMode, &value.HomePageID, &value.HeaderShowTitle, &value.HeaderTagline,
		&value.FooterText, &value.Theme, &value.Actor, &value.CreatedAt,
	); err != nil {
		return Appearance{}, editorialReadError(err)
	}
	value.CreatedAt = value.CreatedAt.UTC()
	return value, nil
}

func (s *editorialStore) saveAppearance(parent context.Context, actor, operationID string, expectedHead int64, input AppearanceInput) (Appearance, error) {
	ctx, conn, release, err := s.connection(parent)
	if err != nil {
		return Appearance{}, err
	}
	defer release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return Appearance{}, editorialMutationKnownError(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	requestHash := appearanceRequestHash(actor, expectedHead, input)

	if err = lockOperation(ctx, tx, operationID); err != nil {
		return Appearance{}, editorialMutationKnownError(err)
	}
	if op, found, e := operationIn(ctx, tx, operationID); e != nil {
		return Appearance{}, e
	} else if found {
		if op.Kind != "appearance.save" || op.ResourceID != AppearanceTarget || op.RequestHash != requestHash {
			return Appearance{}, ErrEditorialConflict
		}
		return loadAppearanceRevision(ctx, tx, op.ResultRevision)
	}

	var current int64
	if err = tx.QueryRow(ctx, "SELECT head_revision FROM rixa.appearance_head WHERE singleton=true FOR UPDATE").Scan(&current); err != nil {
		return Appearance{}, editorialMutationKnownError(err)
	}
	if current != expectedHead {
		return Appearance{}, ErrEditorialConflict
	}
	if input.HomeMode == "page" {
		var kind string
		if err = tx.QueryRow(ctx, "SELECT kind FROM rixa.content_items WHERE id=$1", input.HomePageID).Scan(&kind); err != nil {
			return Appearance{}, editorialMutationKnownError(err)
		}
		if kind != string(ContentPage) {
			return Appearance{}, ErrEditorialInvalid
		}
	}
	next := current + 1
	now := time.Now().UTC().Truncate(time.Microsecond)
	var home any
	if input.HomePageID != "" {
		home = input.HomePageID
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO rixa.appearance_revisions(
			revision,site_title,site_description,site_language,home_mode,home_page_id,
			header_show_title,header_tagline,footer_text,theme,actor,created_at,operation_id
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
	`, next, input.SiteTitle, input.SiteDescription, input.SiteLanguage, input.HomeMode, home,
		input.HeaderShowTitle, input.HeaderTagline, input.FooterText, input.Theme, actor, now, operationID); err != nil {
		return Appearance{}, editorialMutationKnownError(err)
	}
	tag, err := tx.Exec(ctx, "UPDATE rixa.appearance_head SET head_revision=$1 WHERE singleton=true AND head_revision=$2", next, current)
	if err != nil {
		return Appearance{}, editorialMutationKnownError(err)
	}
	if tag.RowsAffected() != 1 {
		return Appearance{}, ErrEditorialConflict
	}
	if err = insertOperation(ctx, tx, EditorialOperation{
		ID: operationID, Kind: "appearance.save", ResourceID: AppearanceTarget, RequestHash: requestHash, ResultRevision: next, CreatedAt: now,
	}); err != nil {
		return Appearance{}, err
	}
	result, err := loadAppearanceRevision(ctx, tx, next)
	if err != nil {
		return Appearance{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Appearance{}, editorialCommitError(err)
	}
	return result, nil
}

func lockOperation(ctx context.Context, tx pgx.Tx, operationID string) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", operationID)
	return err
}

func operationIn(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, operationID string) (EditorialOperation, bool, error) {
	var op EditorialOperation
	err := q.QueryRow(ctx, `
		SELECT operation_id,kind,resource_id,request_hash,result_revision,created_at
		FROM rixa.operations WHERE operation_id=$1
	`, operationID).Scan(&op.ID, &op.Kind, &op.ResourceID, &op.RequestHash, &op.ResultRevision, &op.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return EditorialOperation{}, false, nil
	}
	if err != nil {
		return EditorialOperation{}, false, editorialReadError(err)
	}
	op.CreatedAt = op.CreatedAt.UTC()
	return op, true, nil
}

func insertOperation(ctx context.Context, tx pgx.Tx, op EditorialOperation) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO rixa.operations(operation_id,kind,resource_id,request_hash,result_revision,created_at)
		VALUES($1,$2,$3,$4,$5,$6)
	`, op.ID, op.Kind, op.ResourceID, op.RequestHash, op.ResultRevision, op.CreatedAt)
	if err != nil {
		return editorialMutationKnownError(err)
	}
	return nil
}

func insertMediaRefs(ctx context.Context, tx pgx.Tx, id string, revision int64, refs []ContentMediaRef) error {
	for _, ref := range refs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO rixa.content_media_refs(item_id,revision,asset_id,asset_revision)
			VALUES($1,$2,$3,$4)
		`, id, revision, ref.AssetID, ref.AssetRevision); err != nil {
			return editorialMutationKnownError(err)
		}
	}
	return nil
}

func operationRequestHash(parts ...string) string {
	h := sha256.New()
	var size [8]byte
	for _, part := range parts {
		binary.BigEndian.PutUint64(size[:], uint64(len(part)))
		_, _ = h.Write(size[:])
		_, _ = h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func contentCreateRequestHash(actor string, contentKind ContentKind, title, body string, refs []ContentMediaRef) string {
	parts := []string{"content.create", actor, string(contentKind), title, body}
	for _, ref := range refs {
		parts = append(parts, ref.AssetID, strconv.FormatInt(ref.AssetRevision, 10))
	}
	return operationRequestHash(parts...)
}

func contentRequestHash(kind, actor, resource string, expected int64, title, body string, refs []ContentMediaRef) string {
	parts := []string{kind, actor, resource, strconv.FormatInt(expected, 10), title, body}
	for _, ref := range refs {
		parts = append(parts, ref.AssetID, strconv.FormatInt(ref.AssetRevision, 10))
	}
	return operationRequestHash(parts...)
}

func appearanceRequestHash(actor string, expected int64, input AppearanceInput) string {
	return operationRequestHash(
		"appearance.save", actor, strconv.FormatInt(expected, 10),
		input.SiteTitle, input.SiteDescription, input.SiteLanguage, input.HomeMode, input.HomePageID,
		strconv.FormatBool(input.HeaderShowTitle), input.HeaderTagline, input.FooterText, input.Theme,
	)
}

func randText() string {
	return cryptorand.Text()
}

func editorialReadError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ErrEditorialNotFound
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	}
	return errors.Join(ErrEditorialUnavailable, err)
}

func editorialMutationKnownError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ErrEditorialNotFound
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	}
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) {
		switch pgerr.Code {
		case "23505", "23503", "23514":
			return errors.Join(ErrEditorialConflict, err)
		default:
			return errors.Join(ErrEditorialUnavailable, err)
		}
	}
	return errors.Join(ErrEditorialUnavailable, err)
}

func editorialCommitError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return errors.Join(ErrEditorialUnknownOutcome, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.Join(ErrEditorialUnknownOutcome, context.DeadlineExceeded)
	}
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) {
		return errors.Join(ErrEditorialUnavailable, err)
	}
	return errors.Join(ErrEditorialUnknownOutcome, fmt.Errorf("commit: %w", err))
}
