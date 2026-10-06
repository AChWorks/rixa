// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/AChWorks/achrix"
	shell "github.com/AChWorks/achrix/admin"
	"github.com/AChWorks/achrix/audit"
	"github.com/AChWorks/achrix/identity"
	identityadmin "github.com/AChWorks/achrix/identity/admin"
	"github.com/AChWorks/achrix/media"
	mediaadmin "github.com/AChWorks/achrix/media/admin"
	"github.com/AChWorks/achrix/multisite"
)

type SiteRuntime struct {
	ID        string
	Origin    string
	App       *achrix.Application
	Identity  *identity.Service
	Media     *media.Service
	Editorial   *EditorialService
	Publication *PublicationService
	Handler     http.Handler

	adminPrincipal achrix.Principal
	operator       achrix.Principal
}

type ControlRuntime struct {
	App            *achrix.Application
	Identity       *identity.Service
	Handler        http.Handler
	AdminPrincipal achrix.Principal

	web           *identity.Web
	authenticator shell.Authenticator
}

type routerRuntime struct {
	app     *achrix.Application
	service *multisite.Service
}

type Runtime struct {
	Config  RuntimeConfig
	Control *ControlRuntime
	Sites   map[string]*SiteRuntime
	Handler http.Handler

	router         *routerRuntime
	controlService *ControlService
	applications   []*achrix.Application
	mu             sync.Mutex
	started        bool
}

func BuildRuntime(config RuntimeConfig, logger *slog.Logger) (*Runtime, error) {
	if logger == nil {
		logger = slog.Default()
	}
	controlAdmin, err := Principal(config.Control.AdminPrincipal)
	if err != nil {
		return nil, err
	}

	readable := make(map[string]bool, len(config.Sites))
	manageable := make(map[string]bool, len(config.Sites))
	for _, site := range config.Sites {
		readable[site.ID] = true
		if !site.Disabled {
			manageable[site.ID] = true
		}
	}

	control, err := buildControl(config, controlAdmin, readable, manageable, logger)
	if err != nil {
		return nil, err
	}

	r := &Runtime{
		Config:  config,
		Control: control,
		Sites:   make(map[string]*SiteRuntime, len(config.Sites)),
	}
	r.applications = append(r.applications, control.App)

	for _, site := range config.Sites {
		if site.Disabled {
			continue
		}
		adminPrincipal, e := Principal(site.AdminPrincipal)
		if e != nil {
			return nil, e
		}
		operator := operatorPrincipal(controlAdmin, site.ID)
		s, e := buildSite(config.Config, site, adminPrincipal, operator, logger)
		if e != nil {
			return nil, fmt.Errorf("site %s: %w", site.ID, e)
		}
		r.Sites[site.ID] = s
		r.applications = append(r.applications, s.App)
	}

	if len(config.Sites) > 1 {
		router, e := buildRouter(config.Config, config.Sites, logger)
		if e != nil {
			return nil, e
		}
		r.router = router
		r.applications = append(r.applications, router.app)
	}
	r.controlService = &ControlService{control: control, sites: r.Sites}
	controlSurface := newControlSurface(config.Config.Sites, r.controlService)
	controlShell, err := shell.New(
		shell.Config{Origin: config.Control.Origin, AuthPath: "/auth", Language: config.Language},
		control.authenticator,
		control.App,
		controlSurface,
	)
	if err != nil {
		return nil, fmt.Errorf("control admin: %w", err)
	}
	control.Handler = managementHandler(control.web, controlShell)
	r.Handler = newIngressHandler(r)
	return r, nil
}

func buildControl(config RuntimeConfig, adminPrincipal achrix.Principal, readable, manageable map[string]bool, logger *slog.Logger) (*ControlRuntime, error) {
	auditModule, err := audit.NewPostgres(config.Control.DSN, audit.Config{}, logger)
	if err != nil {
		return nil, fmt.Errorf("control audit: %w", err)
	}
	identityModule, err := identity.NewPostgres(config.Control.DSN, identity.Config{}, logger)
	if err != nil {
		return nil, fmt.Errorf("control identity: %w", err)
	}
	productModule := newControlModule()
	app, err := achrix.New(
		achrix.Config{StartupTimeout: config.StartupTimeout, ShutdownTimeout: config.ShutdownTimeout, Logger: logger},
		controlPolicy(adminPrincipal, readable, manageable),
		auditModule,
		identityModule,
		productModule,
	)
	if err != nil {
		return nil, fmt.Errorf("control composition: %w", err)
	}
	auditService, err := audit.NewService(app, auditModule)
	if err != nil {
		return nil, fmt.Errorf("control audit service: %w", err)
	}
	identityService, err := identity.NewService(app, identityModule, auditService)
	if err != nil {
		return nil, fmt.Errorf("control identity service: %w", err)
	}
	web, err := identity.NewWeb(identityService, config.Control.Origin)
	if err != nil {
		return nil, fmt.Errorf("control identity web: %w", err)
	}
	authenticator, err := identityadmin.Authenticator(web)
	if err != nil {
		return nil, fmt.Errorf("control authenticator: %w", err)
	}
	return &ControlRuntime{
		App:            app,
		Identity:       identityService,
		AdminPrincipal: adminPrincipal,
		web:            web,
		authenticator:  authenticator,
	}, nil
}

func buildSite(config Config, site ResolvedSite, adminPrincipal, operator achrix.Principal, logger *slog.Logger) (*SiteRuntime, error) {
	auditModule, err := audit.NewPostgres(site.DSN, audit.Config{}, logger)
	if err != nil {
		return nil, fmt.Errorf("audit: %w", err)
	}
	identityModule, err := identity.NewPostgres(site.DSN, identity.Config{}, logger)
	if err != nil {
		return nil, fmt.Errorf("identity: %w", err)
	}
	mediaModule, err := media.NewPostgres(site.DSN, media.Config{StorageRoot: site.MediaRoot}, logger)
	if err != nil {
		return nil, fmt.Errorf("media: %w", err)
	}
	productModule := newSiteModule()
	app, err := achrix.New(
		achrix.Config{StartupTimeout: config.StartupTimeout, ShutdownTimeout: config.ShutdownTimeout, Logger: logger},
		sitePolicy(adminPrincipal, operator),
		auditModule,
		identityModule,
		mediaModule,
		productModule,
	)
	if err != nil {
		return nil, fmt.Errorf("composition: %w", err)
	}
	auditService, err := audit.NewService(app, auditModule)
	if err != nil {
		return nil, fmt.Errorf("audit service: %w", err)
	}
	identityService, err := identity.NewService(app, identityModule, auditService)
	if err != nil {
		return nil, fmt.Errorf("identity service: %w", err)
	}
	mediaService, err := media.NewService(app, mediaModule)
	if err != nil {
		return nil, fmt.Errorf("media service: %w", err)
	}
	editorialService, err := newEditorialService(app, mediaService, site.DSN)
	if err != nil {
		return nil, fmt.Errorf("editorial service: %w", err)
	}
	publicationService, err := newPublicationService(site, app, editorialService, mediaService)
	if err != nil {
		return nil, fmt.Errorf("publication service: %w", err)
	}
	web, err := identity.NewWeb(identityService, site.Origin)
	if err != nil {
		return nil, fmt.Errorf("identity web: %w", err)
	}
	authenticator, err := identityadmin.Authenticator(web)
	if err != nil {
		return nil, fmt.Errorf("authenticator: %w", err)
	}
	accounts, err := identityadmin.New(identityService)
	if err != nil {
		return nil, fmt.Errorf("accounts surface: %w", err)
	}
	library, err := mediaadmin.New(mediaService)
	if err != nil {
		return nil, fmt.Errorf("media surface: %w", err)
	}
	contentSurface, err := newContentSurface(editorialService, publicationService)
	if err != nil {
		return nil, fmt.Errorf("content surface: %w", err)
	}
	appearanceSurface, err := newAppearanceSurface(editorialService)
	if err != nil {
		return nil, fmt.Errorf("appearance surface: %w", err)
	}
	adminShell, err := shell.New(
		shell.Config{Origin: site.Origin, AuthPath: "/auth", Language: config.Language},
		authenticator,
		app,
		accounts,
		library,
		contentSurface,
		appearanceSurface,
	)
	if err != nil {
		return nil, fmt.Errorf("admin: %w", err)
	}
	management := managementHandler(web, adminShell)
	return &SiteRuntime{
		ID:             site.ID,
		Origin:         site.Origin,
		App:            app,
		Identity:       identityService,
		Media:          mediaService,
		Editorial:      editorialService,
		Publication:    publicationService,
		Handler:        siteRequestHandler(management, publicationService),
		adminPrincipal: adminPrincipal,
		operator:       operator,
	}, nil
}

func buildRouter(config Config, sites []ResolvedSite, logger *slog.Logger) (*routerRuntime, error) {
	inventory := make([]multisite.Site, 0, len(sites))
	authorities := make(map[string]bool, len(sites))
	for _, site := range sites {
		authority, err := originAuthority(site.Origin)
		if err != nil {
			return nil, ErrConfiguration
		}
		authorities[authority] = true
		inventory = append(inventory, multisite.Site{
			ID:          multisite.SiteID(site.ID),
			Authorities: []string{authority},
			Disabled:    site.Disabled,
		})
	}
	module, err := multisite.New(multisite.Config{Sites: inventory})
	if err != nil {
		return nil, fmt.Errorf("multisite: %w", err)
	}
	app, err := achrix.New(
		achrix.Config{StartupTimeout: config.StartupTimeout, ShutdownTimeout: config.ShutdownTimeout, Logger: logger},
		routingPolicy(authorities),
		module,
	)
	if err != nil {
		return nil, fmt.Errorf("multisite composition: %w", err)
	}
	service, err := multisite.NewService(app, module)
	if err != nil {
		return nil, fmt.Errorf("multisite service: %w", err)
	}
	return &routerRuntime{app: app, service: service}, nil
}

func managementHandler(web *identity.Web, adminShell *shell.Shell) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/auth/", http.StripPrefix("/auth", web.Handler()))
	mux.Handle("/admin", adminShell.Handler())
	mux.Handle("/admin/", adminShell.Handler())
	return mux
}

func siteRequestHandler(management http.Handler, publication *PublicationService) http.Handler {
	if publication == nil {
		return management
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin" || strings.HasPrefix(r.URL.Path, "/admin/") ||
			r.URL.Path == "/auth" || strings.HasPrefix(r.URL.Path, "/auth/") {
			management.ServeHTTP(w, r)
			return
		}
		publication.ServeHTTP(w, r)
	})
}

func operatorPrincipal(control achrix.Principal, siteID string) achrix.Principal {
	return achrix.Principal(fmt.Sprintf("rixa.operator:%s:%s", control, siteID))
}

func (r *Runtime) UsesMultiSite() bool {
	return r != nil && r.router != nil
}

func (r *Runtime) Start(parent context.Context) error {
	if r == nil {
		return ErrConfiguration
	}
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return achrix.ErrNotReady
	}
	r.mu.Unlock()

	ctx, cancel := context.WithTimeout(parent, r.Config.StartupTimeout)
	defer cancel()
	started := make([]*achrix.Application, 0, len(r.applications))
	for _, app := range r.applications {
		if err := app.Start(ctx); err != nil {
			return errors.Join(err, r.stopStarted(started))
		}
		started = append(started, app)
	}
	for _, app := range started {
		if err := app.Ready(ctx); err != nil {
			return errors.Join(err, r.stopStarted(started))
		}
	}
	for _, site := range r.Sites {
		if err := site.Editorial.Ready(ctx); err != nil {
			return errors.Join(fmt.Errorf("site %s editorial: %w", site.ID, err), r.stopStarted(started))
		}
		if site.Publication != nil {
			if err := site.Publication.Ready(); err != nil {
				return errors.Join(fmt.Errorf("site %s publication: %w", site.ID, err), r.stopStarted(started))
			}
		}
	}
	r.mu.Lock()
	r.started = true
	r.mu.Unlock()
	return nil
}

func (r *Runtime) stopStarted(apps []*achrix.Application) error {
	ctx, cancel := context.WithTimeout(context.Background(), r.Config.ShutdownTimeout)
	defer cancel()
	var result error
	for i := len(apps) - 1; i >= 0; i-- {
		result = errors.Join(result, apps[i].Shutdown(ctx))
	}
	return result
}

func (r *Runtime) Shutdown(parent context.Context) error {
	if r == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, r.Config.ShutdownTimeout)
	defer cancel()
	var result error
	for i := len(r.applications) - 1; i >= 0; i-- {
		result = errors.Join(result, r.applications[i].Shutdown(ctx))
	}
	r.mu.Lock()
	r.started = false
	r.mu.Unlock()
	return result
}
