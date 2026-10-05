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
	targets, err := config.DatabaseTargets(getenv)
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

func BootstrapAdmin(parent context.Context, config Config, scope, login, password string, getenv func(string) (string, bool), logger *slog.Logger) (BootstrapResult, error) {
	if login == "" || password == "" {
		return BootstrapResult{}, fmt.Errorf("%w: bootstrap credentials", ErrConfiguration)
	}
	target, err := config.BootstrapDatabase(scope, getenv)
	if err != nil {
		return BootstrapResult{}, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	auditModule, err := audit.NewPostgres(target.DSN, audit.Config{}, logger)
	if err != nil {
		return BootstrapResult{}, err
	}
	identityModule, err := identity.NewPostgres(target.DSN, identity.Config{}, logger)
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

	startCtx, startCancel := context.WithTimeout(parent, config.StartupTimeout)
	defer startCancel()
	if err = app.Start(startCtx); err != nil {
		return BootstrapResult{}, err
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), config.ShutdownTimeout)
		defer stopCancel()
		_ = app.Shutdown(stopCtx)
	}()
	if err = app.Ready(startCtx); err != nil {
		return BootstrapResult{}, err
	}

	lookupCtx, lookupCancel := context.WithTimeout(parent, time.Second)
	existing, lookupErr := service.LookupAccount(lookupCtx, bootstrapPrincipal, login)
	lookupCancel()
	if lookupErr == nil {
		return BootstrapResult{Account: existing}, nil
	}
	if !errors.Is(lookupErr, identity.ErrNotFound) {
		return BootstrapResult{}, lookupErr
	}

	account, createErr := service.CreateAccount(parent, bootstrapPrincipal, login, password)
	if createErr == nil {
		return BootstrapResult{Account: account, Created: true}, nil
	}

	// A failed mutation acknowledgement is never blindly replayed. Reconcile the
	// exact login through Identity's bounded metadata-only lookup.
	reconcileCtx, reconcileCancel := context.WithTimeout(context.WithoutCancel(parent), time.Second)
	defer reconcileCancel()
	reconciled, reconcileErr := service.LookupAccount(reconcileCtx, bootstrapPrincipal, login)
	if reconcileErr == nil {
		return BootstrapResult{Account: reconciled, Reconciled: true}, nil
	}
	return BootstrapResult{}, createErr
}

type ControlService struct {
	control *ControlRuntime
	sites   map[string]*SiteRuntime
}

func (r *Runtime) ControlService() *ControlService {
	if r == nil {
		return nil
	}
	return &ControlService{control: r.Control, sites: r.Sites}
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
