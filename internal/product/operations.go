// SPDX-License-Identifier: MPL-2.0

package product

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/AChWorks/achrix"
	"github.com/AChWorks/achrix/audit"
	"github.com/AChWorks/achrix/identity"
	"github.com/AChWorks/achrix/media"
)

const migrationTimeout = 30 * time.Second

func Migrate(parent context.Context, config Config, getenv func(string) (string, bool)) error {
	if err := validateDeclaredMediaRoots(config.Sites); err != nil {
		return err
	}
	targets, err := config.DatabaseTargets(parent, getenv)
	if err != nil {
		return err
	}
	for _, target := range targets {
		ctx, cancel := context.WithTimeout(parent, migrationTimeout)
		err = audit.Migrate(ctx, target.DSN)
		if err == nil {
			err = identity.Migrate(ctx, target.DSN)
		}
		if err == nil && target.Kind == DatabaseSite {
			err = media.Migrate(ctx, target.DSN)
		}
		if err == nil && target.Kind == DatabaseSite {
			err = migrateEditorial(ctx, target.DSN, target.ID)
		}
		cancel()
		if err != nil {
			return fmt.Errorf("migrate %s: %w", target.ID, err)
		}
	}
	return nil
}

type BootstrapResult struct {
	Account    identity.Account
	Created    bool
	Reconciled bool
}

type bootstrapApplication interface {
	Start(context.Context) error
	Ready(context.Context) error
	Shutdown(context.Context) error
}

type bootstrapAccounts interface {
	CreateAccount(context.Context, achrix.Principal, string, string) (identity.Account, error)
	LookupAccount(context.Context, achrix.Principal, string) (identity.Account, error)
}

func BootstrapAdmin(parent context.Context, config Config, scope, login, password string, getenv func(string) (string, bool), logger *slog.Logger) (BootstrapResult, error) {
	if login == "" || password == "" {
		return BootstrapResult{}, fmt.Errorf("%w: bootstrap credentials", ErrConfiguration)
	}
	if err := validateDeclaredMediaRoots(config.Sites); err != nil {
		return BootstrapResult{}, err
	}
	if _, err := config.DatabaseTargets(parent, getenv); err != nil {
		return BootstrapResult{}, err
	}
	target, err := config.BootstrapDatabase(scope, getenv)
	if err != nil {
		return BootstrapResult{}, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	auditModule, err := audit.NewPostgres(target.DSN, audit.Config{
		MaxConns: config.Resources.Audit.MaxConns, MaxOperations: config.Resources.Audit.MaxOperations,
	}, logger)
	if err != nil {
		return BootstrapResult{}, err
	}
	identityModule, err := identity.NewPostgres(target.DSN, identity.Config{
		MaxConns: config.Resources.Identity.MaxConns, MaxOperations: config.Resources.Identity.MaxOperations,
	}, logger)
	if err != nil {
		return BootstrapResult{}, err
	}
	app, err := achrix.New(
		achrix.Config{StartupTimeout: config.StartupTimeout, ShutdownTimeout: config.ShutdownTimeout, Logger: logger},
		bootstrapPolicy(login),
		auditModule,
		identityModule,
	)
	if err != nil {
		return BootstrapResult{}, err
	}
	auditService, err := audit.NewService(app, auditModule)
	if err != nil {
		return BootstrapResult{}, err
	}
	service, err := identity.NewService(app, identityModule, auditService)
	if err != nil {
		return BootstrapResult{}, err
	}
	return bootstrapAdmin(parent, config, login, password, app, service)
}

func bootstrapAdmin(parent context.Context, config Config, login, password string, app bootstrapApplication, service bootstrapAccounts) (result BootstrapResult, err error) {
	startCtx, startCancel := context.WithTimeout(parent, config.StartupTimeout)
	defer startCancel()
	if err = app.Start(startCtx); err != nil {
		return BootstrapResult{}, err
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.WithoutCancel(parent), config.ShutdownTimeout)
		defer stopCancel()
		err = errors.Join(err, app.Shutdown(stopCtx))
	}()
	if err = app.Ready(startCtx); err != nil {
		return BootstrapResult{}, err
	}

	account, createErr := service.CreateAccount(parent, bootstrapPrincipal, login, password)
	if createErr == nil {
		return BootstrapResult{Account: account, Created: true}, nil
	}
	if !ambiguousCreateOutcome(createErr) {
		return BootstrapResult{}, createErr
	}

	// A genuinely unknown mutation acknowledgement is never blindly replayed.
	// Reconcile only the retained exact login and return no credential/session data.
	reconcileCtx, reconcileCancel := context.WithTimeout(context.WithoutCancel(parent), time.Second)
	defer reconcileCancel()
	reconciled, reconcileErr := service.LookupAccount(reconcileCtx, bootstrapPrincipal, login)
	if reconcileErr == nil {
		return BootstrapResult{Account: reconciled, Reconciled: true}, nil
	}
	return BootstrapResult{}, errors.Join(createErr, reconcileErr)
}

func ambiguousCreateOutcome(err error) bool {
	return errors.Is(err, identity.ErrUnavailable) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}

type ControlService struct {
	control *ControlRuntime
	sites   map[string]*SiteRuntime
}

func (r *Runtime) ControlService() *ControlService {
	if r == nil {
		return nil
	}
	return r.controlService
}

func (s *ControlService) site(parent context.Context, actor achrix.Principal, siteID string) (*SiteRuntime, error) {
	if s == nil || s.control == nil {
		return nil, ErrConfiguration
	}
	ctx, cancel := context.WithTimeout(parent, time.Second)
	defer cancel()
	if err := s.control.App.Authorize(ctx, actor, CapabilityControlSiteManage, siteID); err != nil {
		return nil, err
	}
	site := s.sites[siteID]
	if site == nil {
		return nil, achrix.ErrDenied
	}
	return site, nil
}

func (s *ControlService) CreateSiteAccount(ctx context.Context, actor achrix.Principal, siteID, login, password string) (identity.Account, error) {
	site, err := s.site(ctx, actor, siteID)
	if err != nil {
		return identity.Account{}, err
	}
	return site.Identity.CreateAccount(ctx, site.operator, login, password)
}

func (s *ControlService) SiteAccount(ctx context.Context, actor achrix.Principal, siteID, accountID string) (identity.Account, error) {
	site, err := s.site(ctx, actor, siteID)
	if err != nil {
		return identity.Account{}, err
	}
	return site.Identity.Account(ctx, site.operator, accountID)
}

func (s *ControlService) CreateSiteMedia(ctx context.Context, actor achrix.Principal, siteID, filename string, input io.Reader) (media.Asset, error) {
	site, err := s.site(ctx, actor, siteID)
	if err != nil {
		return media.Asset{}, err
	}
	return site.Media.Create(ctx, site.operator, filename, input)
}

func (s *ControlService) ListSiteMedia(ctx context.Context, actor achrix.Principal, siteID, cursor string, limit int) (media.Page, error) {
	site, err := s.site(ctx, actor, siteID)
	if err != nil {
		return media.Page{}, err
	}
	return site.Media.List(ctx, site.operator, cursor, limit)
}

func (s *ControlService) ReadSiteMedia(ctx context.Context, actor achrix.Principal, siteID, assetID string) (media.Asset, []byte, error) {
	site, err := s.site(ctx, actor, siteID)
	if err != nil {
		return media.Asset{}, nil, err
	}
	var output bytes.Buffer
	asset, err := site.Media.Read(ctx, site.operator, assetID, &output)
	if err != nil {
		return media.Asset{}, nil, err
	}
	return asset, output.Bytes(), nil
}

func (s *ControlService) ListSiteContent(ctx context.Context, actor achrix.Principal, siteID string) ([]ContentSummary, error) {
	site, err := s.site(ctx, actor, siteID)
	if err != nil {
		return nil, err
	}
	return site.Editorial.List(ctx, site.operator)
}

func (s *ControlService) CreateSiteContent(ctx context.Context, actor achrix.Principal, siteID, operationID string, kind ContentKind, title, body string) (ContentRevision, error) {
	site, err := s.site(ctx, actor, siteID)
	if err != nil {
		return ContentRevision{}, err
	}
	return site.Editorial.Create(ctx, site.operator, operationID, kind, title, body)
}

func (s *ControlService) SaveSiteContent(ctx context.Context, actor achrix.Principal, siteID, operationID, contentID string, expectedHead int64, title, body string) (ContentRevision, error) {
	site, err := s.site(ctx, actor, siteID)
	if err != nil {
		return ContentRevision{}, err
	}
	return site.Editorial.Save(ctx, site.operator, operationID, contentID, expectedHead, title, body)
}

func (s *ControlService) SiteAppearance(ctx context.Context, actor achrix.Principal, siteID string) (Appearance, error) {
	site, err := s.site(ctx, actor, siteID)
	if err != nil {
		return Appearance{}, err
	}
	return site.Editorial.Appearance(ctx, site.operator)
}

func (s *ControlService) SaveSiteAppearance(ctx context.Context, actor achrix.Principal, siteID, operationID string, expectedHead int64, input AppearanceInput) (Appearance, error) {
	site, err := s.site(ctx, actor, siteID)
	if err != nil {
		return Appearance{}, err
	}
	return site.Editorial.SaveAppearance(ctx, site.operator, operationID, expectedHead, input)
}

func (s *ControlService) ApplySitePublication(ctx context.Context, actor achrix.Principal, siteID string, request PublicationRequest) (PublicationResult, error) {
	site, err := s.site(ctx, actor, siteID)
	if err != nil {
		return PublicationResult{}, err
	}
	if site.Publication == nil {
		return PublicationResult{}, ErrEditorialUnavailable
	}
	return site.Publication.Apply(ctx, site.operator, request)
}

func (s *ControlService) SitePublicationOperation(ctx context.Context, actor achrix.Principal, siteID, operationID string) (PublicationOperation, error) {
	site, err := s.site(ctx, actor, siteID)
	if err != nil {
		return PublicationOperation{}, err
	}
	if site.Publication == nil {
		return PublicationOperation{}, ErrEditorialUnavailable
	}
	return site.Publication.Operation(ctx, site.operator, operationID)
}
