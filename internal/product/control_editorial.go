// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"

	"github.com/AChWorks/achrix"
)

// Selected-site editorial facades revalidate the control grant on every call.
// The browser's principal remains a control identity; only this trusted product
// boundary delegates to the selected site's origin-qualified operator.
func (s *ControlService) ListSiteContentPage(ctx context.Context, actor achrix.Principal, siteID, cursor string, limit int) (ContentListPage, error) {
	site, err := s.site(ctx, actor, siteID)
	if err != nil {
		return ContentListPage{}, err
	}
	return site.Editorial.ListPage(ctx, site.operator, cursor, limit)
}

func (s *ControlService) HeadSiteContent(ctx context.Context, actor achrix.Principal, siteID, id string) (ContentRevision, error) {
	site, err := s.site(ctx, actor, siteID)
	if err != nil {
		return ContentRevision{}, err
	}
	return site.Editorial.Head(ctx, site.operator, id)
}

func (s *ControlService) PreviewSiteContent(ctx context.Context, actor achrix.Principal, siteID, id string, revision int64) (ContentRevision, error) {
	site, err := s.site(ctx, actor, siteID)
	if err != nil {
		return ContentRevision{}, err
	}
	return site.Editorial.Preview(ctx, site.operator, id, revision)
}

func (s *ControlService) SiteContentRevisionsPage(ctx context.Context, actor achrix.Principal, siteID, id, cursor string, limit int) (RevisionPage, error) {
	site, err := s.site(ctx, actor, siteID)
	if err != nil {
		return RevisionPage{}, err
	}
	return site.Editorial.RevisionsPage(ctx, site.operator, id, cursor, limit)
}

func (s *ControlService) SiteEditorialOperation(ctx context.Context, actor achrix.Principal, siteID, operationID string) (EditorialOperation, error) {
	site, err := s.site(ctx, actor, siteID)
	if err != nil {
		return EditorialOperation{}, err
	}
	return site.Editorial.Operation(ctx, site.operator, operationID)
}
