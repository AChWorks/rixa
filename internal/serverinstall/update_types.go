// SPDX-License-Identifier: MPL-2.0

package serverinstall

import "context"

// UpdateDraft selects an authenticated payload for one already-owned native
// installation. It contains no replacement account, database or TLS inputs.
// The original installation and its frozen manager remain separately retained.
type UpdateDraft struct {
	OperationID           string `json:"operation_id"`
	InstallationOperation string `json:"installation_operation"`
	ArchivePath           string `json:"archive"`
	BundlePath            string `json:"bundle"`
	Release               string `json:"release"`
	SourceCommit          string `json:"source_commit"`
}

// UpdateFacade is the additive native update boundary shared by CLI/TUI and
// future authorized adapters. Older install-only facades retain their contract.
type UpdateFacade interface {
	SaveUpdateDraft(context.Context, UpdateDraft) (Result, error)
	LoadUpdateDraft(context.Context, string) (UpdateDraft, error)
	ReviewUpdate(context.Context, string) (Result, error)
	ApplyUpdate(context.Context, string, string) (Result, error)
	ResumeUpdate(context.Context, string) (Result, error)
	UpdateStatus(context.Context, string) (Result, error)
	RemoveUpdatedInstallation(context.Context, string) (Result, error)
}
