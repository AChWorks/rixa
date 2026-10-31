// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/AChWorks/achrix"
	"github.com/AChWorks/achrix/identity"
	"github.com/jackc/pgx/v5"
)

// Site grants are deliberately product-owned. Identity owns account status and
// sessions; AChrix Core owns authorization admission; Rixa owns CMS rights.
// A site database cannot confer a grant to a different site.
const (
	CapabilitySiteGrantManage = "rixa.site-grant.manage"
	SiteGrantCollectionTarget = "site-grants"
)

var (
	ErrSiteGrantInvalid     = errors.New("invalid site grant")
	ErrSiteGrantNotFound    = errors.New("site grant not found")
	ErrSiteGrantConflict    = errors.New("site grant revision conflict")
	ErrSiteGrantUnavailable = errors.New("site grant unavailable")
)

type SiteGrant struct {
	AccountID  string
	Permission string
	Allowed    bool
	Revision   int64
	UpdatedBy  string
	UpdatedAt  time.Time
}

// SiteAccess exposes administrator-only, revision-fenced grant management.
// It also provides fresh per-call CMS authorization for real site accounts.
// No public transport/automatic account enrollment is introduced by this API.
type SiteAccess struct {
	dsn      string
	operator achrix.Principal
	app      *achrix.Application
	accounts *identity.Service
	slots    chan struct{}
}

func newSiteAccess(dsn string, operator achrix.Principal) *SiteAccess {
	return &SiteAccess{dsn: dsn, operator: operator, slots: make(chan struct{}, 8)}
}

// bind is called during single-threaded runtime assembly, before Start/ingress.
func (s *SiteAccess) bind(app *achrix.Application, accounts *identity.Service) {
	s.app, s.accounts = app, accounts
}

func validSiteGrantPermission(permission string) bool {
	return permission == CapabilityContentRead || permission == CapabilityContentEdit
}

func requiredSiteGrant(capability, resource string) string {
	switch capability {
	case CapabilityContentList:
		if resource == ContentCollectionTarget {
			return CapabilityContentRead
		}
	case CapabilityContentRead, CapabilityContentPreview:
		if resource == ContentCollectionTarget || validEditorialID(resource) {
			return CapabilityContentRead
		}
	case CapabilityContentEdit:
		if resource == ContentCollectionTarget || validEditorialID(resource) {
			return CapabilityContentEdit
		}
	}
	return ""
}

func (s *SiteAccess) withConnection(parent context.Context) (context.Context, *pgx.Conn, func(), error) {
	if s == nil || s.dsn == "" {
		return nil, nil, nil, ErrSiteGrantUnavailable
	}
	select {
	case s.slots <- struct{}{}:
	case <-parent.Done():
		return nil, nil, nil, parent.Err()
	default:
		return nil, nil, nil, ErrSiteGrantUnavailable
	}
	ctx, cancel := context.WithTimeout(parent, time.Second)
	conn, err := pgx.Connect(ctx, s.dsn)
	if err != nil {
		cancel()
		<-s.slots
		return nil, nil, nil, fmt.Errorf("%w: connect", ErrSiteGrantUnavailable)
	}
	done := func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
		_ = conn.Close(closeCtx)
		closeCancel()
		cancel()
		<-s.slots
	}
	return ctx, conn, done, nil
}

// memberAuthorize never trusts a claimed role or site supplied by a transport.
// It checks Identity's CURRENT enabled state and the CURRENT site-owned grant.
// Denial and dependency outage remain distinct, and no result is cached.
func (s *SiteAccess) memberAuthorize(ctx context.Context, actor achrix.Principal, capability, resource string) error {
	permission := requiredSiteGrant(capability, resource)
	if permission == "" || !validEditorialID(string(actor)) {
		return achrix.ErrDenied
	}
	if s == nil || s.accounts == nil {
		return ErrSiteGrantUnavailable
	}
	account, err := s.accounts.Account(ctx, s.operator, string(actor))
	if errors.Is(err, identity.ErrNotFound) {
		return achrix.ErrDenied
	}
	if err != nil {
		return fmt.Errorf("%w: account status", ErrSiteGrantUnavailable)
	}
	if !account.Enabled {
		return achrix.ErrDenied
	}

	queryCtx, conn, done, err := s.withConnection(ctx)
	if err != nil {
		return err
	}
	defer done()
	var allowed bool
	err = conn.QueryRow(queryCtx, "SELECT allowed FROM rixa.site_grants WHERE account_id=$1 AND permission=$2", account.ID, permission).Scan(&allowed)
	if errors.Is(err, pgx.ErrNoRows) {
		return achrix.ErrDenied
	}
	if err != nil {
		return fmt.Errorf("%w: grant lookup", ErrSiteGrantUnavailable)
	}
	if !allowed {
		return achrix.ErrDenied
	}
	return nil
}

func (s *SiteAccess) authorizeManager(ctx context.Context, actor achrix.Principal) error {
	if s == nil || s.app == nil || s.accounts == nil {
		return ErrSiteGrantUnavailable
	}
	authCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return s.app.Authorize(authCtx, actor, CapabilitySiteGrantManage, SiteGrantCollectionTarget)
}

// Grant reads are administrator-only, separate from ordinary member content reads.
func (s *SiteAccess) Grant(ctx context.Context, actor achrix.Principal, accountID, permission string) (SiteGrant, error) {
	if !validEditorialID(accountID) || !validSiteGrantPermission(permission) {
		return SiteGrant{}, ErrSiteGrantInvalid
	}
	if err := s.authorizeManager(ctx, actor); err != nil {
		return SiteGrant{}, err
	}
	queryCtx, conn, done, err := s.withConnection(ctx)
	if err != nil {
		return SiteGrant{}, err
	}
	defer done()
	var grant SiteGrant
	err = conn.QueryRow(queryCtx, `SELECT account_id,permission,allowed,revision,updated_by,updated_at
        FROM rixa.site_grants WHERE account_id=$1 AND permission=$2`, accountID, permission).
		Scan(&grant.AccountID, &grant.Permission, &grant.Allowed, &grant.Revision, &grant.UpdatedBy, &grant.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return SiteGrant{}, ErrSiteGrantNotFound
	}
	if err != nil {
		return SiteGrant{}, fmt.Errorf("%w: read", ErrSiteGrantUnavailable)
	}
	return grant, nil
}

// Set requires a known existing Identity account and an exact revision:
// expectedRevision=0 creates only an absent entry; otherwise CAS updates it.
// On an uncertain commit result the caller must inspect Grant, never replay
// with a fresh precondition. Grant history is recorded in the same transaction.
func (s *SiteAccess) Set(ctx context.Context, actor achrix.Principal, accountID, permission string, allowed bool, expectedRevision int64) (SiteGrant, error) {
	if !validEditorialID(accountID) || !validSiteGrantPermission(permission) || expectedRevision < 0 {
		return SiteGrant{}, ErrSiteGrantInvalid
	}
	if err := s.authorizeManager(ctx, actor); err != nil {
		return SiteGrant{}, err
	}
	account, err := s.accounts.Account(ctx, s.operator, accountID)
	if errors.Is(err, identity.ErrNotFound) {
		return SiteGrant{}, ErrSiteGrantInvalid
	}
	if err != nil {
		return SiteGrant{}, fmt.Errorf("%w: account status", ErrSiteGrantUnavailable)
	}
	if account.ID != accountID {
		return SiteGrant{}, ErrSiteGrantUnavailable
	}

	queryCtx, conn, done, err := s.withConnection(ctx)
	if err != nil {
		return SiteGrant{}, err
	}
	defer done()
	tx, err := conn.Begin(queryCtx)
	if err != nil {
		return SiteGrant{}, fmt.Errorf("%w: begin", ErrSiteGrantUnavailable)
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()

	grant := SiteGrant{AccountID: accountID, Permission: permission, Allowed: allowed, UpdatedBy: string(actor)}
	if expectedRevision == 0 {
		err = tx.QueryRow(queryCtx, `INSERT INTO rixa.site_grants(account_id,permission,allowed,revision,updated_by,updated_at)
            VALUES($1,$2,$3,1,$4,CURRENT_TIMESTAMP) ON CONFLICT DO NOTHING
            RETURNING revision,updated_at`, accountID, permission, allowed, string(actor)).
			Scan(&grant.Revision, &grant.UpdatedAt)
	} else {
		err = tx.QueryRow(queryCtx, `UPDATE rixa.site_grants SET allowed=$3,revision=revision+1,
            updated_by=$4,updated_at=CURRENT_TIMESTAMP
            WHERE account_id=$1 AND permission=$2 AND revision=$5
            RETURNING revision,updated_at`, accountID, permission, allowed, string(actor), expectedRevision).
			Scan(&grant.Revision, &grant.UpdatedAt)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return SiteGrant{}, ErrSiteGrantConflict
	}
	if err != nil {
		return SiteGrant{}, fmt.Errorf("%w: write", ErrSiteGrantUnavailable)
	}
	if _, err = tx.Exec(queryCtx, `INSERT INTO rixa.site_grant_history
        (account_id,permission,allowed,revision,changed_by,changed_at)
        VALUES($1,$2,$3,$4,$5,$6)`,
		accountID, permission, allowed, grant.Revision, string(actor), grant.UpdatedAt); err != nil {
		return SiteGrant{}, fmt.Errorf("%w: history", ErrSiteGrantUnavailable)
	}
	if err = tx.Commit(queryCtx); err != nil {
		return SiteGrant{}, fmt.Errorf("%w: commit outcome unknown; inspect grant", ErrSiteGrantUnavailable)
	}
	return grant, nil
}
