// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"time"

	"github.com/AChWorks/achrix"
	"github.com/jackc/pgx/v5"
)

const (
	CapabilityThemeRead    = "rixa.theme.read"
	CapabilityThemeEdit    = "rixa.theme.edit"
	CapabilityThemePreview = "rixa.theme.preview"
	CapabilityThemePublish = "rixa.theme.publish"
)

type ThemeRevision struct {
	Revision   int64       `json:"revision"`
	Bundle     ThemeBundle `json:"bundle"`
	SourceHash string      `json:"source_hash"`
	Actor      string      `json:"actor"`
	CreatedAt  time.Time   `json:"created_at"`
}

type ThemeHistoryItem struct {
	Revision   int64     `json:"revision"`
	SourceHash string    `json:"source_hash"`
	Actor      string    `json:"actor"`
	CreatedAt  time.Time `json:"created_at"`
}

type ThemeService struct {
	editorial        *EditorialService
	publication      *PublicationService
	managementOrigin string
	previewOrigin    string
	previewMu        sync.Mutex
	previews         map[string]*themePreview
}

func newThemeService(editorial *EditorialService, managementOrigin, previewOrigin string) *ThemeService {
	return &ThemeService{editorial: editorial, managementOrigin: managementOrigin, previewOrigin: previewOrigin}
}
func (s *ThemeService) enabled() bool {
	return s != nil && s.editorial != nil && s.managementOrigin != "" && s.previewOrigin != ""
}
func (s *ThemeService) authorize(ctx context.Context, actor achrix.Principal, capability string) error {
	if !s.enabled() {
		return ErrEditorialUnavailable
	}
	return s.editorial.authorize(ctx, actor, capability, ThemeTarget)
}

func (s *ThemeService) Head(ctx context.Context, actor achrix.Principal) (ThemeRevision, error) {
	if err := s.authorize(ctx, actor, CapabilityThemeRead); err != nil {
		return ThemeRevision{}, err
	}
	queryCtx, conn, release, err := s.editorial.store.connection(ctx)
	if err != nil {
		return ThemeRevision{}, err
	}
	defer release()
	var head int64
	if err = conn.QueryRow(queryCtx, "SELECT head_revision FROM rixa.theme_head WHERE singleton=true").Scan(&head); err != nil {
		return ThemeRevision{}, editorialReadError(err)
	}
	if head == 0 {
		return ThemeRevision{Bundle: defaultThemeBundle()}, nil
	}
	return readThemeRevision(queryCtx, conn, head)
}

func readThemeRevision(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, revision int64) (ThemeRevision, error) {
	var value ThemeRevision
	var source []byte
	err := q.QueryRow(ctx, `SELECT revision,bundle,source_hash,actor,created_at FROM rixa.theme_revisions WHERE revision=$1`, revision).
		Scan(&value.Revision, &source, &value.SourceHash, &value.Actor, &value.CreatedAt)
	if err != nil {
		return ThemeRevision{}, editorialReadError(err)
	}
	if err = json.Unmarshal(source, &value.Bundle); err != nil || value.Bundle.sourceHash() != value.SourceHash {
		return ThemeRevision{}, ErrEditorialUnavailable
	}
	// Values are validated on first creation and when loaded: upgrades/restore
	// must not turn previously rejected source into executable material.
	if err = value.Bundle.validate(); err != nil {
		return ThemeRevision{}, ErrEditorialUnavailable
	}
	value.CreatedAt = value.CreatedAt.UTC()
	return value, nil
}

func (s *ThemeService) Revision(ctx context.Context, actor achrix.Principal, revision int64) (ThemeRevision, error) {
	if revision < 1 {
		return ThemeRevision{}, ErrEditorialInvalid
	}
	if err := s.authorize(ctx, actor, CapabilityThemeRead); err != nil {
		return ThemeRevision{}, err
	}
	queryCtx, conn, release, err := s.editorial.store.connection(ctx)
	if err != nil {
		return ThemeRevision{}, err
	}
	defer release()
	return readThemeRevision(queryCtx, conn, revision)
}

// loadRevisionTrusted is used only after PublicationService has already
// checked the actual actor's Theme publication authorization.
func (s *ThemeService) loadRevisionTrusted(ctx context.Context, revision int64) (ThemeRevision, error) {
	if !s.enabled() || revision < 1 {
		return ThemeRevision{}, ErrEditorialUnavailable
	}
	queryCtx, conn, release, err := s.editorial.store.connection(ctx)
	if err != nil {
		return ThemeRevision{}, err
	}
	defer release()
	return readThemeRevision(queryCtx, conn, revision)
}

func (s *ThemeService) History(ctx context.Context, actor achrix.Principal) ([]ThemeHistoryItem, error) {
	if err := s.authorize(ctx, actor, CapabilityThemeRead); err != nil {
		return nil, err
	}
	queryCtx, conn, release, err := s.editorial.store.connection(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	rows, err := conn.Query(queryCtx, "SELECT revision,source_hash,actor,created_at FROM rixa.theme_revisions ORDER BY revision DESC LIMIT 50")
	if err != nil {
		return nil, editorialReadError(err)
	}
	defer rows.Close()
	result := make([]ThemeHistoryItem, 0)
	for rows.Next() {
		var item ThemeHistoryItem
		if err = rows.Scan(&item.Revision, &item.SourceHash, &item.Actor, &item.CreatedAt); err != nil {
			return nil, editorialReadError(err)
		}
		item.CreatedAt = item.CreatedAt.UTC()
		result = append(result, item)
	}
	if err = rows.Err(); err != nil {
		return nil, editorialReadError(err)
	}
	return result, nil
}

// SaveDraft is a coherent, immutable transaction. It shares the Rixa
// editorial source gate and operation ledger, never touching public output.
func (s *ThemeService) SaveDraft(ctx context.Context, actor achrix.Principal, operationID string, expected int64, bundle ThemeBundle) (ThemeRevision, error) {
	if !validOperationID(operationID) || expected < 0 {
		return ThemeRevision{}, ErrEditorialInvalid
	}
	if err := s.authorize(ctx, actor, CapabilityThemeEdit); err != nil {
		return ThemeRevision{}, err
	}
	if err := bundle.validate(); err != nil {
		return ThemeRevision{}, err
	}
	releaseGate, err := s.editorial.acquirePublicationSource(ctx)
	if err != nil {
		return ThemeRevision{}, err
	}
	defer releaseGate()

	source, err := json.Marshal(bundle)
	if err != nil {
		return ThemeRevision{}, ErrEditorialInvalid
	}
	hash := bundle.sourceHash()
	requestHash := operationRequestHash("theme.save", string(actor), strconv.FormatInt(expected, 10), hash)
	queryCtx, conn, release, err := s.editorial.store.connection(ctx)
	if err != nil {
		return ThemeRevision{}, err
	}
	defer release()
	tx, err := conn.Begin(queryCtx)
	if err != nil {
		return ThemeRevision{}, editorialMutationKnownError(err)
	}
	defer tx.Rollback(context.Background())
	if err = lockOperation(queryCtx, tx, operationID); err != nil {
		return ThemeRevision{}, editorialMutationKnownError(err)
	}
	if prior, found, e := operationIn(queryCtx, tx, operationID); e != nil {
		return ThemeRevision{}, e
	} else if found {
		if prior.Kind != "theme.save" || prior.ResourceID != ThemeTarget || prior.RequestHash != requestHash {
			return ThemeRevision{}, ErrEditorialConflict
		}
		return readThemeRevision(queryCtx, tx, prior.ResultRevision)
	}
	var head int64
	if err = tx.QueryRow(queryCtx, "SELECT head_revision FROM rixa.theme_head WHERE singleton=true FOR UPDATE").Scan(&head); err != nil {
		return ThemeRevision{}, editorialReadError(err)
	}
	if head != expected {
		return ThemeRevision{}, ErrEditorialConflict
	}
	next := head + 1
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err = tx.Exec(queryCtx, `INSERT INTO rixa.theme_revisions(revision,bundle,source_hash,actor,created_at,operation_id)
        VALUES($1,$2,$3,$4,$5,$6)`, next, source, hash, string(actor), now, operationID); err != nil {
		return ThemeRevision{}, editorialMutationKnownError(err)
	}
	if _, err = tx.Exec(queryCtx, "UPDATE rixa.theme_head SET head_revision=$1 WHERE singleton=true", next); err != nil {
		return ThemeRevision{}, editorialMutationKnownError(err)
	}
	if err = insertOperation(queryCtx, tx, EditorialOperation{
		ID: operationID, Kind: "theme.save", ResourceID: ThemeTarget, RequestHash: requestHash,
		ResultRevision: next, CreatedAt: now,
	}); err != nil {
		return ThemeRevision{}, err
	}
	if err = tx.Commit(queryCtx); err != nil {
		return ThemeRevision{}, editorialCommitError(err)
	}
	return ThemeRevision{Revision: next, Bundle: bundle, SourceHash: hash, Actor: string(actor), CreatedAt: now}, nil
}

func (s *ThemeService) Operation(ctx context.Context, actor achrix.Principal, id string) (EditorialOperation, error) {
	if !validOperationID(id) {
		return EditorialOperation{}, ErrEditorialInvalid
	}
	if err := s.authorize(ctx, actor, CapabilityThemeRead); err != nil {
		return EditorialOperation{}, err
	}
	result, err := s.editorial.store.operation(ctx, id)
	if err != nil {
		return EditorialOperation{}, err
	}
	if result.Kind != "theme.save" || result.ResourceID != ThemeTarget {
		return EditorialOperation{}, ErrEditorialNotFound
	}
	return result, nil
}

// PublishRevision is also RepublishRevision: a new durable publication
// operation, never a pointer rewrite. Generation CAS and receipt replay are
// implemented by the existing PublicationService.
func (s *ThemeService) PublishRevision(ctx context.Context, actor achrix.Principal, operationID string, revision int64, expectedGeneration string) (PublicationResult, error) {
	if !validOperationID(operationID) || revision < 1 {
		return PublicationResult{}, ErrEditorialInvalid
	}
	if err := s.authorize(ctx, actor, CapabilityThemePublish); err != nil {
		return PublicationResult{}, err
	}
	if s.publication == nil {
		return PublicationResult{}, ErrEditorialUnavailable
	}
	return s.publication.Apply(ctx, actor, PublicationRequest{
		OperationID: operationID, ExpectedGeneration: expectedGeneration, ThemeRevision: revision,
	})
}

func (s *ThemeService) PublishedRevision() int64 {
	if s == nil || s.publication == nil {
		return 0
	}
	state := s.publication.state.Load()
	if state == nil || len(state.generations) == 0 {
		return 0
	}
	return state.generations[0].manifest.ThemeRevision
}
