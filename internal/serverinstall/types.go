// SPDX-License-Identifier: MPL-2.0

// Package serverinstall owns Rixa's explicit native operator profile. It is not
// a generic privileged runner. A Draft contains credentials and must never be
// logged or returned as a public operation result.
package serverinstall

import (
	"context"
	"errors"
)

var (
	ErrInput          = errors.New("invalid native installation input")
	ErrAuthority      = errors.New("native management requires the infrastructure operator")
	ErrManagerChanged = errors.New("the executing manager image or its trusted pathname changed; restart the retained trusted manager")
	ErrBusy           = errors.New("another native management operation is active")
	ErrState          = errors.New("native operation state requires inspection")
	ErrPrerequisite   = errors.New("native installation prerequisite is unavailable")
)

type Draft struct {
	OperationID     string `json:"operation_id"`
	ArchivePath     string `json:"archive"`
	BundlePath      string `json:"bundle"`
	Release         string `json:"release"`
	SourceCommit    string `json:"source_commit"`
	ControlOrigin   string `json:"control_origin"`
	SiteOrigin      string `json:"site_origin"`
	Listen          string `json:"listen"`
	TLSCertPath     string `json:"tls_cert"`
	TLSKeyPath      string `json:"tls_key"`
	ControlDSN      string `json:"control_dsn"`
	SiteDSN         string `json:"site_dsn"`
	ControlLogin    string `json:"control_login"`
	SiteLogin       string `json:"site_login"`
	ControlPassword string `json:"control_password"`
	SitePassword    string `json:"site_password"`
	Language        string `json:"language"`
}

type Problem struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Result struct {
	OperationKind         string    `json:"operation_kind,omitempty"`
	InstallationOperation string    `json:"installation_operation,omitempty"`
	ActiveRelease         string    `json:"active_release,omitempty"`
	ReviewToken           string    `json:"review_token,omitempty"`
	OperationID           string    `json:"operation_id"`
	State                 string    `json:"state"`
	Stage                 string    `json:"stage,omitempty"`
	Code                  string    `json:"code"`
	Message               string    `json:"message,omitempty"`
	Release               string    `json:"release,omitempty"`
	AdminURL              string    `json:"admin_url,omitempty"`
	Problems              []Problem `json:"problems,omitempty"`
	Actions               []string  `json:"actions,omitempty"`
}

// Facade is the single backend consumed by direct CLI and terminal presentation.
type Facade interface {
	SaveDraft(context.Context, Draft) (Result, error)
	LoadDraft(context.Context, string) (Draft, error)
	Review(context.Context, string) (Result, error)
	Apply(context.Context, string, string) (Result, error)
	Resume(context.Context, string) (Result, error)
	Status(context.Context, string) (Result, error)
	Remove(context.Context, string) (Result, error)
}

// InstallationEffects is the optional strict effect seam for callers authorized
// only for one original installation operation. Unlike Facade's CLI convenience
// methods, these methods never follow a pending or active child update. The
// operation binding is checked while holding the native lifecycle effect lock.
// Callers requiring this scope must fail closed when the seam is unavailable.
type InstallationEffects interface {
	ResumeInstallation(context.Context, string) (Result, error)
	RemoveInstallation(context.Context, string) (Result, error)
}

var _ InstallationEffects = (*Manager)(nil)
