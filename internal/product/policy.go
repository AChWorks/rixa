// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"

	"github.com/AChWorks/achrix"
	"github.com/AChWorks/achrix/audit"
	"github.com/AChWorks/achrix/identity"
	"github.com/AChWorks/achrix/media"
	"github.com/AChWorks/achrix/multisite"
)

const (
	CapabilityControlInventoryRead = "rixa.control.inventory.read"
	CapabilityControlSiteManage    = "rixa.control.site.manage"
	CapabilityContentList          = "rixa.content.list"
	CapabilityContentRead          = "rixa.content.read"
	CapabilityContentEdit          = "rixa.content.edit"
	CapabilityContentPreview       = "rixa.content.preview"
	CapabilityContentPublishIntent = "rixa.content.publication-intent"
	CapabilityPublicationApply     = "rixa.publication.apply"
	CapabilityAppearanceRead       = "rixa.appearance.read"
	CapabilityAppearanceEdit       = "rixa.appearance.edit"
	CapabilityOperationRead        = "rixa.editorial.operation.read"
)

const routerPrincipal achrix.Principal = "rixa.router"

func sitePolicy(siteAdmin, controlOperator achrix.Principal) achrix.Policy {
	return achrix.PolicyFunc(func(ctx context.Context, actor achrix.Principal, capability, resource string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if actor == identity.PublicPrincipal {
			if capability == identity.Authentication && resource == "" {
				return nil
			}
			return achrix.ErrDenied
		}
		isAdmin := actor == siteAdmin
		isOperator := controlOperator != "" && actor == controlOperator
		if !isAdmin && !isOperator {
			return achrix.ErrDenied
		}
		switch capability {
		case audit.Append:
			return nil
		case identity.AccountCreate:
			if resource == "" {
				return nil
			}
		case identity.AccountRead, identity.AccountLookup, identity.CredentialSet, identity.AccountSetEnabled, identity.SessionRevokeAll:
			return nil
		case identity.PasswordChange:
			if isAdmin && resource == string(actor) {
				return nil
			}
		case media.Create, media.List, media.Reconcile:
			if resource == media.LibraryTarget {
				return nil
			}
		case media.Read, media.PreparePublicImage:
			return nil
		case CapabilityContentList:
			if resource == ContentCollectionTarget {
				return nil
			}
		case CapabilityContentRead, CapabilityContentEdit, CapabilityContentPreview, CapabilityContentPublishIntent:
			if resource == ContentCollectionTarget || validEditorialID(resource) {
				return nil
			}
		case CapabilityPublicationApply:
			if resource == PublicationTarget {
				return nil
			}
		case CapabilityAppearanceRead, CapabilityAppearanceEdit:
			if resource == AppearanceTarget {
				return nil
			}
		case CapabilityOperationRead:
			if resource == OperationCollectionTarget {
				return nil
			}
		}
		return achrix.ErrDenied
	})
}

func controlPolicy(admin achrix.Principal, readable, manageable map[string]bool) achrix.Policy {
	return achrix.PolicyFunc(func(ctx context.Context, actor achrix.Principal, capability, resource string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if actor == identity.PublicPrincipal {
			if capability == identity.Authentication && resource == "" {
				return nil
			}
			return achrix.ErrDenied
		}
		if actor != admin {
			return achrix.ErrDenied
		}
		switch capability {
		case audit.Append:
			return nil
		case identity.PasswordChange:
			if resource == string(actor) {
				return nil
			}
		case CapabilityControlInventoryRead:
			if resource == "" {
				return nil
			}
		case CapabilityControlSiteManage:
			if readable[resource] && manageable[resource] {
				return nil
			}
		}
		return achrix.ErrDenied
	})
}

func routingPolicy(authorities map[string]bool) achrix.Policy {
	return achrix.PolicyFunc(func(ctx context.Context, actor achrix.Principal, capability, resource string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if actor == routerPrincipal && capability == multisite.Resolve && authorities[resource] {
			return nil
		}
		return achrix.ErrDenied
	})
}

func bootstrapPolicy(login string) achrix.Policy {
	return achrix.PolicyFunc(func(ctx context.Context, actor achrix.Principal, capability, resource string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if actor != bootstrapPrincipal {
			return achrix.ErrDenied
		}
		switch capability {
		case identity.AccountCreate:
			if resource == "" {
				return nil
			}
		case identity.AccountLookup:
			if resource == login {
				return nil
			}
		case audit.Append:
			return nil
		}
		return achrix.ErrDenied
	})
}

const bootstrapPrincipal achrix.Principal = "rixa.bootstrap"
