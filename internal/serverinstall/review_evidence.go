// SPDX-License-Identifier: MPL-2.0
package serverinstall

import "maps"

type reviewedEvidence struct {
	archive, cert, key, helper string
	databases, transport       map[string]string
}

func captureReviewedEvidence(j *journal) reviewedEvidence {
	return reviewedEvidence{j.ArchiveSHA256, j.CertSHA256, j.KeySHA256, j.HelperSHA256, maps.Clone(j.DatabaseTargets), maps.Clone(j.TransportEnv)}
}
func changedReviewedEvidence(before reviewedEvidence, j *journal) []Problem {
	var problems []Problem
	add := func(field, message string) { problems = append(problems, Problem{field, "evidence-changed", message}) }
	if before.archive != j.ArchiveSHA256 {
		add("archive", "The authenticated archive bytes changed. Review the exact selected release again.")
	}
	if before.cert != j.CertSHA256 {
		add("tls_cert", "The supplied certificate changed. Review the intended TLS material again.")
	}
	if before.key != j.KeySHA256 {
		add("tls_key", "The supplied private key changed. Review the intended TLS material again.")
	}
	if !maps.Equal(before.databases, j.DatabaseTargets) {
		add("control_dsn", "The physical control/site database targets changed. Inspect both database connections and review again.")
	}
	if before.helper != j.HelperSHA256 {
		add("archive", "The trusted verification helper changed. Review the selected helper and artifact again.")
	}
	// nil and empty maps are equivalent across omitempty persistence round trips.
	if !maps.Equal(before.transport, j.TransportEnv) {
		add("archive", "The verification proxy/CA transport settings changed. Review the intended transport environment again.")
	}
	return problems
}
func (m *Manager) rejectReview(j *journal, code string, problems []Problem) (Result, error) {
	j.State, j.Stage, j.Code, j.Message = "blocked", "review", code, "Reviewed prerequisites changed. Correct the reported field and run Review again before applying."
	if len(problems) > 0 {
		j.Message = "Review required (" + problems[0].Field + "): " + problems[0].Message
	}
	j.ReviewToken = ""
	if e := m.save(j); e != nil {
		return Result{}, e
	}
	r := result(j)
	r.Problems = problems
	return r, ErrPrerequisite
}
