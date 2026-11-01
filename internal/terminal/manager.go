// SPDX-License-Identifier: MPL-2.0

package terminal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/AChWorks/rixa/internal/serverinstall"
)

type Options struct {
	Input        *os.File
	Output       io.Writer
	Plain        bool
	NoColor      bool
	Version      string
	OperationID  string
	Prefill      *serverinstall.Draft
	FetchRelease func(context.Context, string, string, string) (FetchedArtifact, error)
}

// Run shows only actions supplied by the shared native backend. Closing this
// facade does not cancel a previously queued persistent installation operation.
func Run(ctx context.Context, backend serverinstall.Facade, options Options) error {
	return runManagement(ctx, backend, options, nil)
}

func runManagement(ctx context.Context, backend serverinstall.Facade, options Options, profile managementProfile) (err error) {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	if backend == nil || options.Output == nil {
		return errors.New("native management backend and output are required")
	}
	s, err := newSession(options.Input, options.Output, options.Plain, options.NoColor)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.close()) }()
	id := options.OperationID
	if id == "" {
		b := make([]byte, 8)
		if _, err = rand.Read(b); err != nil {
			return err
		}
		prefix := "install-"
		if profile != nil {
			prefix = profile.kind() + "-"
		}
		id = prefix + hex.EncodeToString(b)
	}
	draft := serverinstall.Draft{OperationID: id, Listen: "0.0.0.0:8443", Language: "en", ControlLogin: "admin", SiteLogin: "admin"}
	if profile != nil {
		draft = serverinstall.Draft{OperationID: id}
	}
	if options.Prefill != nil {
		draft = *options.Prefill
		if draft.OperationID != "" {
			if options.OperationID != "" && options.OperationID != draft.OperationID {
				return errors.New("prefill operation_id must match --operation")
			}
			id = draft.OperationID
		}
		draft.OperationID = id
		if profile == nil && draft.Listen == "" {
			draft.Listen = "0.0.0.0:8443"
		}
		if profile == nil && draft.Language == "" {
			draft.Language = "en"
		}
	}
	for _, problem := range serverinstall.ValidateDraft(draft) {
		if problem.Field == "operation_id" {
			return errors.New(problem.Message)
		}
	}

	current := serverinstall.Result{OperationID: id, State: "not checked", Code: "not_checked", Actions: []string{"configure", "status"}}
	if profile != nil {
		current = profile.annotate(current)
	}
	notice := ""
	if options.OperationID != "" {
		current, err = backend.Status(ctx, id)
		if err != nil {
			notice = publicError(err, draft)
			if len(current.Actions) == 0 {
				current.Actions = []string{"status"}
			}
		}
		if restored, loadErr := backend.LoadDraft(ctx, id); loadErr == nil {
			draft = restored
		}
	}
	selected := 0
	lastCheck := time.Now()
	dirty := false
	reviewedToken := ""
	for {
		allowed := []string{}
		for _, action := range current.Actions {
			if action != "apply" || reviewedToken != "" {
				allowed = append(allowed, action)
			}
		}
		if dirty {
			allowed = nil
			for _, a := range current.Actions {
				if a == "configure" || a == "status" {
					allowed = append(allowed, a)
				}
			}
		}
		actions := menuActions(allowed)
		if profile != nil {
			actions = profile.actions(allowed)
		}
		if selected >= len(actions) {
			selected = 0
		}
		displayNotice := notice
		if dirty {
			displayNotice = "Unsaved edits: Configure to save before Review/Apply. " + displayNotice
		}
		lines := menuLines(s, options.Version, current, displayNotice, draft, actions, selected)
		title := "RIXA / Server management"
		if profile != nil {
			title = profile.title()
		}
		if err = s.paint(title, lines, "Up/Down or Tab · Enter · F1 help · Ctrl-C exit"); err != nil {
			return err
		}
		k, readErr := s.readKey(ctx)
		if readErr != nil {
			return readErr
		}
		if k.name == "quit" {
			return nil
		}
		if s.full && (s.width < 40 || s.height < 12) {
			s.resize()
			continue
		}
		switch k.name {
		case "tick":
			if s.resize() {
				continue
			}
			if current.State == "applying" && time.Since(lastCheck) >= time.Second {
				current, err = backend.Status(ctx, id)
				lastCheck = time.Now()
				if err != nil {
					notice = publicError(err, draft)
				}
			}
			continue
		case "up", "back":
			selected = (selected + len(actions) - 1) % len(actions)
			continue
		case "down", "next":
			selected = (selected + 1) % len(actions)
			continue
		case "help":
			help := []string{
				"Configure stores inputs privately; Review checks prerequisites.",
				"Apply queues the exact reviewed operation. A queue is not success.",
				"Refresh inspects actual retained progress and service observations.",
				"Closing the menu leaves an already queued operation running.",
				"Removal preserves configuration, credentials, databases and site data.",
				"Use manage update --help to inspect the separate update interface.",
				"No purge, PostgreSQL provisioner or panel adapter is offered here.",
				"The terminal interface always uses English, independently of web language.",
			}
			if profile != nil {
				help = profile.help()
			}

			if err = s.paint("RIXA / Help", help, "Enter / Escape returns"); err != nil {
				return err
			}
			if err = s.wait(ctx); err != nil {
				return err
			}
			continue
		case "text":
			if k.text >= "1" && k.text <= "9" {
				n := int(k.text[0] - '1')
				if n < len(actions) {
					selected = n
					continue
				} else {
					continue
				}
			} else {
				continue
			}
		case "enter":
		default:
			continue
		}
		action := actions[selected].id
		switch action {
		case "exit":
			return nil
		case "details":
			detail := overview(options.Version, current, notice)
			if profile != nil {
				detail = append(detail, profile.details()...)
			}
			if err = showDetails(ctx, s, detail, draft); err != nil {
				return err
			}
		case "configure":
			reviewedToken = ""
			var completed bool
			if draft.ArchivePath == "" && options.FetchRelease != nil {
				fetched, fetchErr := fetchRelease(ctx, s, options.FetchRelease, &draft)
				if fetchErr != nil {
					return fetchErr
				}
				if !fetched {
					notice = "Download skipped. You may supply already downloaded archive and bundle paths."
				}
			}
			if profile == nil {
				current, completed, err = configure(ctx, s, backend, &draft, current)
			} else {
				current, completed, err = profile.configure(ctx, s, &draft, current)
			}
			if err != nil {
				return err
			}
			if completed {
				dirty = false
				notice = "Draft saved privately. Review prerequisites before applying."
			} else {
				dirty = true
				notice = "Editing paused; entries are retained in this menu only. Nothing was applied."
			}
		case "review":
			current, err = backend.Review(ctx, id)
			reviewedToken = ""
			if err == nil && current.State == "ready" && current.ReviewToken != "" {
				if restored, loadErr := backend.LoadDraft(ctx, id); loadErr == nil {
					draft = restored
					reviewedToken = current.ReviewToken
				} else {
					err = loadErr
				}
			}
			notice = resultNotice(current, err, draft)
		case "status":
			current, err = backend.Status(ctx, id)
			if current.State != "ready" || current.ReviewToken != "" && current.ReviewToken != reviewedToken {
				reviewedToken = ""
			}
			notice = resultNotice(current, err, draft)
			lastCheck = time.Now()
		case "apply", "resume":
			title := "Start reviewed installation?"
			word := "APPLY"
			if action == "resume" {
				title = "Resume the retained installation?"
				word = "RESUME"
			}
			confirmation := []string{
				"Operation: " + SafeText(id), "Release: " + SafeText(current.Release),
				"Control: " + SafeText(draft.ControlOrigin),
				"Site: " + SafeText(draft.SiteOrigin),
				"Listener: " + SafeText(draft.Listen),
				"Owned user/service: rixa / rixa.service.",
				"Owned paths: /opt/rixa, /etc/rixa, /var/lib/rixa.",
				"Existing PostgreSQL and supplied TLS are reused.",
				"This creates the reviewed user, protected files and persistent service.",
				"Only the exact reviewed operation will be queued.",
				"Closing this menu does not stop an already queued operation.",
			}
			if profile == nil && (draft.SiteBOrigin != "" || draft.SiteBDSN != "" || draft.SiteBLogin != "" || draft.SiteBPassword != "") {
				confirmation = append(confirmation, "Optional site-b: "+SafeText(draft.SiteBOrigin)+"; separate database and administrator.")
			}
			if profile != nil {
				title, word, confirmation = profile.confirmation(action, id, current)
			}
			yes, confirmErr := confirm(ctx, s, title, confirmation, word)
			if confirmErr != nil {
				return confirmErr
			}
			if yes {
				if action == "resume" {
					current, err = backend.Resume(ctx, id)
				} else {
					current, err = backend.Apply(ctx, id, reviewedToken)
				}
				notice = resultNotice(current, err, draft)
			}
		case "remove":
			removal := []string{
				"Operation: " + SafeText(id),
				"Remove only the positively identified Rixa executable/service material.",
				"Keep configuration, credentials, databases, site data and prerequisites.",
				"This stops serving. A separate purge is not available.",
			}
			if profile != nil && profile.kind() == "update" {
				removal = append([]string{
					"Update operation: " + SafeText(id),
					"Installation operation: " + SafeText(current.InstallationOperation),
					"Active release: " + SafeText(current.ActiveRelease),
				}, removal[1:]...)
			}
			yes, confirmErr := confirm(ctx, s, "Remove Rixa executable and service?", removal, "REMOVE")
			if confirmErr != nil {
				return confirmErr
			}
			if yes {
				current, err = backend.Remove(ctx, id)
				notice = resultNotice(current, err, draft)
			}
		}
	}
}

type menuAction struct{ id, label string }

func menuActions(allowed []string) []menuAction {
	choices := []menuAction{{"configure", "Configure installation"}, {"review", "Review prerequisites"}, {"apply", "Apply reviewed installation"}, {"resume", "Resume retained installation"}, {"status", "Refresh status"}, {"remove", "Remove executable and service"}}
	return filterMenuActions(allowed, choices)
}

func filterMenuActions(allowed []string, choices []menuAction) []menuAction {
	var result []menuAction
	for _, choice := range choices {
		for _, action := range allowed {
			if choice.id == action {
				result = append(result, choice)
				break
			}
		}
	}
	// Inspection and exit remain reachable even when an unknown backend state
	// intentionally grants no mutations.
	found := false
	for _, a := range result {
		if a.id == "status" {
			found = true
		}
	}
	if !found {
		result = append(result, menuAction{"status", "Refresh status"})
	}
	return append(result, menuAction{"details", "View operation details"}, menuAction{"exit", "Exit"})
}
func overview(version string, r serverinstall.Result, notice string) []string {
	if version == "" {
		version = "development"
	}
	state := r.State
	if state == "" {
		state = "not checked"
	}
	lines := []string{"Manager: " + SafeText(version) + "  |  Operation: " + SafeText(r.OperationID), "Recorded release: " + SafeText(r.Release), "State: " + SafeText(state) + "  |  Stage: " + SafeText(r.Stage)}
	if r.OperationKind == "update" {
		lines = []string{"Executing manager: " + SafeText(version) + "  |  Update operation: " + SafeText(r.OperationID), "Installation operation: " + SafeText(r.InstallationOperation), "Active release: " + SafeText(r.ActiveRelease), "Candidate release: " + SafeText(r.Release), "State: " + SafeText(state) + "  |  Stage: " + SafeText(r.Stage)}
	}
	if r.OperationKind == "postgres" {
		lines = []string{"Manager: " + SafeText(version) + "  |  PostgreSQL operation: " + SafeText(r.OperationID), "Scope: clean dedicated PostgreSQL prerequisites", "State: " + SafeText(state) + "  |  Stage: " + SafeText(r.Stage)}
	}
	if r.OperationKind == "bootstrap" {
		lines = []string{"Starter manager: " + SafeText(version), "Retained operation: " + SafeText(r.OperationID), "State: " + SafeText(state), "Scope: Rixa native server"}
	}
	if r.AdminURL != "" {
		lines = append(lines, "Administration: "+SafeText(r.AdminURL))
	}
	if r.Message != "" {
		lines = append(lines, "Status: "+SafeText(r.Message))
	}
	if notice != "" {
		lines = append(lines, "Notice: "+SafeText(notice))
	}
	for _, p := range r.Problems {
		lines = append(lines, "["+SafeText(p.Code)+"] "+SafeText(p.Field)+": "+SafeText(p.Message))
	}

	return append(lines, "")
}
func publicError(err error, d serverinstall.Draft) string {
	if err == nil {
		return ""
	}
	value := err.Error()
	for _, secret := range []string{d.ControlDSN, d.SiteDSN, d.SiteBDSN, d.ControlPassword, d.SitePassword, d.SiteBPassword} {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	return SafeText(value)
}
func resultNotice(r serverinstall.Result, err error, d serverinstall.Draft) string {
	if err != nil {
		return publicError(err, d)
	}
	if r.Message != "" {
		return r.Message
	}
	return "Observed " + r.State + " at stage " + r.Stage + "."
}

type field struct {
	key, label, hint string
	secret           bool
	value            *string
}

func draftFields(d *serverinstall.Draft) []field {
	fields := []field{
		{"archive", "Release archive", "Absolute path to the downloaded versioned Linux amd64 archive.", false, &d.ArchivePath},
		{"bundle", "Attestation bundle", "Absolute path to the adjacent .sigstore.json; no signature bypass.", false, &d.BundlePath},
		{"release", "Release tag", "Exact selected tag, for example v0.2.0.", false, &d.Release},
		{"source_commit", "Source commit", "Exact 40-character source commit of the selected tag.", false, &d.SourceCommit},
		{"control_origin", "Control URL", "Distinct HTTPS authority for installation administration.", false, &d.ControlOrigin},
		{"site_origin", "Site URL", "HTTPS authority for the first site; separate from control.", false, &d.SiteOrigin},
		{"listen", "TLS listener", "Supplied certificate and HTTPS origins must match this port.", false, &d.Listen},
		{"tls_cert", "TLS certificate", "Absolute path to a supplied valid certificate for both names.", false, &d.TLSCertPath},
		{"tls_key", "TLS private key", "Absolute path to the protected matching private key.", false, &d.TLSKeyPath},
		{"control_dsn", "Control database connection", "Loopback TCP PostgreSQL URL: explicit user/password/port/database and sslmode=disable.", true, &d.ControlDSN},
		{"site_dsn", "Site database connection", "Separate loopback TCP PostgreSQL18 database URL; same explicit credential rules.", true, &d.SiteDSN},
		{"control_login", "Control administrator login", "Exact initial control login; an unrelated existing login is a conflict.", false, &d.ControlLogin},
		{"control_password", "Control administrator password", "Hidden; empty keeps a previously saved value.", true, &d.ControlPassword},
		{"site_login", "Site administrator login", "Exact initial administrator login for this site.", false, &d.SiteLogin},
		{"site_password", "Site administrator password", "Hidden; empty keeps a previously saved value.", true, &d.SitePassword},
		{"language", "Web interface language", "Choose bundled en or fa for the web interface. The terminal always uses English.", false, &d.Language},
	}
	if d.SiteBOrigin != "" || d.SiteBDSN != "" || d.SiteBLogin != "" || d.SiteBPassword != "" {
		fields = append(fields, []field{
			{"site_b_origin", "Optional second-site URL", "Leave all four second-site fields blank for one site; otherwise use a third distinct HTTPS hostname.", false, &d.SiteBOrigin},
			{"site_b_dsn", "Optional second-site database", "Separate prepared PostgreSQL18 database and role; Ctrl-U explicitly clears a saved secret.", true, &d.SiteBDSN},
			{"site_b_login", "Optional second-site administrator", "Required only when enabling site-b; site accounts stay independent.", false, &d.SiteBLogin},
			{"site_b_password", "Optional second-site password", "Required only when enabling site-b; Ctrl-U explicitly clears a saved secret.", true, &d.SiteBPassword},
		}...)
	}
	return fields
}
func configure(ctx context.Context, s *session, backend serverinstall.Facade, d *serverinstall.Draft, current serverinstall.Result) (serverinstall.Result, bool, error) {
	fields := draftFields(d)
	return configureFields(ctx, s, backend, d, current, fields, initialField(fields, *d), func() []serverinstall.Problem { return serverinstall.ValidateDraftFields(*d) })
}

func configureFields(ctx context.Context, s *session, backend serverinstall.Facade, d *serverinstall.Draft, current serverinstall.Result, fields []field, index int, validate func() []serverinstall.Problem) (serverinstall.Result, bool, error) {
	notice := ""
	for {
		f := fields[index]
		value := *f.value
		clearedSecret := false
		if f.secret {
			value = ""
		}
		first := true
		for {
			if s.full || first {
				visible := SafeText(value)
				if f.secret {
					visible = strings.Repeat("*", min(utf8.RuneCountInString(value), 48))
					if value == "" && *f.value != "" {
						visible = "[saved value; type to replace]"
					}
				}
				lines := []string{fmt.Sprintf("Configure · %d / %d", index+1, len(fields)), "", f.label, f.hint, "", "> " + inputVisible(s, visible)}
				if notice != "" {
					lines = append(lines, "", "[input] "+SafeText(notice))
				}
				if err := s.paint("RIXA / Configure", lines, "Enter / Tab next · Escape back · Ctrl-U clear · F1 help · Ctrl-C exit"); err != nil {
					return current, false, err
				}
				first = false
			}
			k, err := s.readKey(ctx)
			if err != nil {
				return current, false, err
			}
			if k.name == "quit" {
				return current, false, context.Canceled
			}
			if s.full && (s.width < 40 || s.height < 12) {
				s.resize()
				continue
			}
			switch k.name {
			case "tick":
				if s.resize() {
					first = true
				}
				continue
			case "help":
				detail := []string{f.label, f.hint, "", "Review and apply are separate. Existing compatible databases/TLS are required.", "Back preserves entries in this menu; only a complete saved draft survives exit."}
				if notice != "" {
					detail = append(detail, "", "Validation: "+notice)
				}
				if err = showDetails(ctx, s, detail, *d); err != nil {
					return current, false, err
				}
				first = true
				continue
			case "paste-invalid":
				notice = "Paste rejected. Ctrl-C exits. Discarding late input until paste ends; nothing changed."
				first = true
				continue
			case "text", "paste":
				if k.name == "paste" {
					invalid := !utf8.ValidString(k.text)
					for _, r := range k.text {
						if unicode.IsControl(r) {
							invalid = true
						}
					}
					if invalid {
						notice = "Multi-line/control-character paste is not accepted here; use private draft JSON for such a secret. No field was changed."
						first = true
						continue
					}
				}
				if len(value)+len(k.text) > 8192 {
					notice = "Input is limited to 8192 bytes."
					first = true
					continue
				}
				value += k.text
				if !s.full {
					text := SafeText(k.text)
					if f.secret {
						text = "*"
					}
					if _, err = io.WriteString(s.output, text); err != nil {
						return current, false, err
					}
				}
				continue
			case "clear":
				value = ""
				clearedSecret = true
				first = true
				continue
			case "backspace":
				if value != "" {
					_, n := utf8.DecodeLastRuneInString(value)
					value = value[:len(value)-n]
				}
				if !s.full {
					if _, err = io.WriteString(s.output, "\r\nInput edited.\r\n"); err != nil {
						return current, false, err
					}
					first = true
				}
				continue
			case "back", "up":
				if !f.secret || value != "" || clearedSecret {
					*f.value = value
				}
				if index == 0 {
					return current, false, nil
				}
				index--
				notice = ""
				break
			case "enter", "next", "down":
				if !f.secret || value != "" || clearedSecret {
					*f.value = value
				}
				problems := validate()
				bad := ""
				for _, p := range problems {
					if p.Field == f.key {
						bad = p.Message
						break
					}
				}
				if bad != "" {
					notice = publicValue(bad, *d)
					first = true
					continue
				}
				index++
				notice = ""
				if index == len(fields) {
					result, saveErr := backend.SaveDraft(ctx, *d)
					if saveErr == nil {
						return result, true, nil
					}
					current = result
					notice = publicError(saveErr, *d)
					index = len(fields) - 1
					for _, problem := range result.Problems {
						for i, f := range fields {
							if f.key == problem.Field {
								index = i
								notice = publicValue(problem.Message, *d)
								break
							}
						}
						if index != len(fields)-1 {
							break
						}
					}
				}
				break
			default:
				continue
			}
			break
		}
	}
}
func confirm(ctx context.Context, s *session, title string, lines []string, word string) (bool, error) {
	value := ""
	notice := ""
	offset := 0
	firstRender := true
	for {
		summary := lines
		budget := 10000
		if s.full {
			summary = wrapLines(lines, max(1, s.width-2))
			budget = max(1, s.height-4)
		}
		reserved := 3
		if notice != "" {
			reserved++
		}
		available := max(1, budget-reserved)
		if offset >= len(summary) {
			offset = max(0, len(summary)-available)
		}
		end := min(len(summary), offset+available)
		body := append(append([]string{}, summary[offset:end]...), "", "Type "+word+" to confirm, or Escape to return.", "> "+SafeText(value))
		if notice != "" {
			body = append(body, notice)
		}
		if s.full || firstRender {
			if err := s.paint("RIXA / "+title, body, "Up/Down details · Enter confirm · Escape back · Ctrl-C exit"); err != nil {
				return false, err
			}
			firstRender = false
		}

		k, err := s.readKey(ctx)
		if err != nil {
			return false, err
		}
		if k.name == "quit" {
			return false, context.Canceled
		}
		if s.full && (s.width < 40 || s.height < 12) {
			s.resize()
			continue
		}
		switch k.name {
		case "tick":
			s.resize()
		case "up":
			offset = max(0, offset-1)
		case "down", "next":
			if end < len(summary) {
				offset++
			}
		case "back":
			return false, nil
		case "text":
			if len(value) < 32 {
				value += k.text
				if !s.full {
					if _, err = io.WriteString(s.output, SafeText(k.text)); err != nil {
						return false, err
					}
				}
			}
		case "backspace":
			if value != "" {
				_, n := utf8.DecodeLastRuneInString(value)
				value = value[:len(value)-n]
				if !s.full {
					if _, err = io.WriteString(s.output, "\r\nInput edited.\r\n"); err != nil {
						return false, err
					}
					firstRender = true
				}
			}
		case "enter":
			if value == word {
				return true, nil
			}
			notice = "Confirmation did not match. Nothing was applied."
			firstRender = true
		}
	}
}

func publicValue(value string, d serverinstall.Draft) string {
	for _, secret := range []string{d.ControlDSN, d.SiteDSN, d.SiteBDSN, d.ControlPassword, d.SitePassword, d.SiteBPassword} {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	return SafeText(value)
}
func menuLines(s *session, version string, r serverinstall.Result, notice string, d serverinstall.Draft, actions []menuAction, selected int) []string {
	header := overview(version, r, notice)
	budget := 1000
	if s.full {
		budget = max(1, s.height-4)
	}
	if len(header)+len(actions) > budget {
		header = []string{"Recorded release: " + SafeText(r.Release) + " · Manager: " + SafeText(version), "State: " + SafeText(r.State) + " · Stage: " + SafeText(r.Stage)}
		if r.OperationKind == "update" {
			header = []string{"Active: " + SafeText(r.ActiveRelease) + " · Candidate: " + SafeText(r.Release), "Installation: " + SafeText(r.InstallationOperation) + " · Manager: " + SafeText(version), "State: " + SafeText(r.State) + " · Stage: " + SafeText(r.Stage)}
		}
		if r.OperationKind == "postgres" || r.OperationKind == "bootstrap" {
			header = []string{"Manager: " + SafeText(version) + " · Operation: " + SafeText(r.OperationID), "State: " + SafeText(r.State) + " · Stage: " + SafeText(r.Stage)}
		}
		if r.AdminURL != "" && budget >= 10 {
			header = append(header, "Administration: "+SafeText(r.AdminURL))
		}
		if notice != "" || len(r.Problems) > 0 {
			header = append(header, "Warning/details: choose View operation details.")
		}
		header = append(header, "")
	}
	available := max(1, budget-len(header))
	start := 0
	if len(actions) > available {
		start = max(0, selected-available+1)
	}
	for i := start; i < len(actions) && i < start+available; i++ {
		prefix := "  "
		if i == selected {
			prefix = "> "
		}
		header = append(header, fmt.Sprintf("%s%d. %s", prefix, i+1, actions[i].label))
	}
	for i := range header {
		header[i] = publicValue(header[i], d)
	}
	return header
}
func showDetails(ctx context.Context, s *session, lines []string, d serverinstall.Draft) error {
	for i := range lines {
		lines[i] = publicValue(lines[i], d)
	}
	offset := 0
	for {
		visible := lines
		if s.full {
			visible = wrapLines(lines, max(1, s.width-2))
		}
		size := len(visible)
		if s.full {
			size = max(1, s.height-5)
		}
		if offset >= len(visible) {
			offset = max(0, len(visible)-size)
		}
		end := min(len(visible), offset+size)
		if err := s.paint("RIXA / Operation details", visible[offset:end], "Up/Down scroll · Enter/Escape back · Ctrl-C exit"); err != nil {
			return err
		}
		k, err := s.readKey(ctx)
		if err != nil {
			return err
		}
		switch k.name {
		case "quit":
			return context.Canceled
		case "enter", "back":
			return nil
		case "up":
			offset = max(0, offset-1)
		case "down", "next":
			if end < len(visible) {
				offset++
			}
		case "tick":
			s.resize()
		}
	}
}

// wrapLines preserves every rune of diagnostic text. It is intentionally not a
// claim of Persian/CJK shaping or full terminal display-cell certification.
func wrapLines(lines []string, width int) []string {
	if width < 1 {
		width = 1
	}
	var out []string
	for _, line := range lines {
		runes := []rune(line)
		for len(runes) > width {
			out = append(out, string(runes[:width]))
			runes = runes[width:]
		}
		out = append(out, string(runes))
	}
	return out
}
func inputVisible(s *session, value string) string {
	if !s.full {
		return value
	}
	limit := max(4, s.width-4)
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return "..." + string(runes[len(runes)-(limit-3):])
}
