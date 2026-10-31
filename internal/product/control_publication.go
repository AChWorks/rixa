// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"

	"github.com/AChWorks/achrix"
)

// The direct-site and control-selected editor share one publication presenter.
// Only the transport-specific adapter supplies the trusted identity/site scope.
type publicationIntentEditor interface {
	SetPublicationIntent(context.Context, achrix.Principal, string, string, int64, int64, int64) (ContentRevision, error)
	ClearPublicationIntent(context.Context, achrix.Principal, string, string, int64, int64) (ContentRevision, error)
}

type publicationPresenter interface {
	State(context.Context, achrix.Principal, string, ContentKind) (PublicationState, error)
	Apply(context.Context, achrix.Principal, PublicationRequest) (PublicationResult, error)
	Operation(context.Context, achrix.Principal, string) (PublicationOperation, error)
}

type directPublication struct{ service *PublicationService }

func (s directPublication) State(_ context.Context, _ achrix.Principal, id string, kind ContentKind) (PublicationState, error) {
	return s.service.State(id, kind), nil
}
func (s directPublication) Apply(ctx context.Context, actor achrix.Principal, request PublicationRequest) (PublicationResult, error) {
	return s.service.Apply(ctx, actor, request)
}
func (s directPublication) Operation(ctx context.Context, actor achrix.Principal, id string) (PublicationOperation, error) {
	return s.service.Operation(ctx, actor, id)
}

type controlPublicationManager interface {
	SetSitePublicationIntent(context.Context, achrix.Principal, string, string, string, int64, int64, int64) (ContentRevision, error)
	ClearSitePublicationIntent(context.Context, achrix.Principal, string, string, string, int64, int64) (ContentRevision, error)
	SitePublicationState(context.Context, achrix.Principal, string, string, ContentKind) (PublicationState, error)
	ApplySitePublication(context.Context, achrix.Principal, string, PublicationRequest) (PublicationResult, error)
	SitePublicationOperation(context.Context, achrix.Principal, string, string) (PublicationOperation, error)
}

type selectedPublication struct {
	manager controlPublicationManager
	siteID  string
}

func (s selectedPublication) SetPublicationIntent(ctx context.Context, actor achrix.Principal, op, id string, head, version, revision int64) (ContentRevision, error) {
	return s.manager.SetSitePublicationIntent(ctx, actor, s.siteID, op, id, head, version, revision)
}
func (s selectedPublication) ClearPublicationIntent(ctx context.Context, actor achrix.Principal, op, id string, head, version int64) (ContentRevision, error) {
	return s.manager.ClearSitePublicationIntent(ctx, actor, s.siteID, op, id, head, version)
}
func (s selectedPublication) State(ctx context.Context, actor achrix.Principal, id string, kind ContentKind) (PublicationState, error) {
	return s.manager.SitePublicationState(ctx, actor, s.siteID, id, kind)
}
func (s selectedPublication) Apply(ctx context.Context, actor achrix.Principal, request PublicationRequest) (PublicationResult, error) {
	return s.manager.ApplySitePublication(ctx, actor, s.siteID, request)
}
func (s selectedPublication) Operation(ctx context.Context, actor achrix.Principal, id string) (PublicationOperation, error) {
	return s.manager.SitePublicationOperation(ctx, actor, s.siteID, id)
}
