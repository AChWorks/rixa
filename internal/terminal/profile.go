// SPDX-License-Identifier: MPL-2.0

package terminal

import (
	"context"
	"github.com/AChWorks/rixa/internal/serverinstall"
)

// Profiles change task presentation while sharing one session, input decoder,
// local review fencing, editable form and confirmation implementation.
type managementProfile interface {
	kind() string
	title() string
	annotate(serverinstall.Result) serverinstall.Result
	actions([]string) []menuAction
	help() []string
	details() []string
	configure(context.Context, *session, *serverinstall.Draft, serverinstall.Result) (serverinstall.Result, bool, error)
	confirmation(string, string, serverinstall.Result) (string, string, []string)
}

func (u *updateAdapter) kind() string                          { return "update" }
func (u *updateAdapter) title() string                         { return "RIXA / Update management" }
func (u *updateAdapter) actions(allowed []string) []menuAction { return updateMenuActions(allowed) }
func (u *updateAdapter) details() []string                     { return nil }
func (u *updateAdapter) confirmation(action, id string, r serverinstall.Result) (string, string, []string) {
	return updateConfirmation(action, id, r, u.installation)
}
func (u *updateAdapter) help() []string {
	return []string{
		"Configure selects a candidate for one existing owning installation.",
		"Review binds active state, candidate, manager and recovery conditions.",
		"Apply queues the exact reviewed update; a queue is not success.",
		"Refresh inspects retained progress; uncertain effects require inspection.",
		"Resume is offered only after the backend proves it is safe.",
		"Keep the original installation and its frozen manager.",
		"A binary reversal does not roll back database changes.",
		"The terminal interface always uses English, independently of web language.",
	}
}
