// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"strconv"
	"syscall"
	"time"

	"github.com/AChWorks/rixa/internal/product"
	"github.com/jackc/pgx/v5/pgconn"
)

// addSiteRuntimeTask holds a stage input on a private pipe, not in argv.
type addSiteRuntimeTask struct {
	OperationID     string            `json:"operation_id"`
	UID             uint32            `json:"uid"`
	GID             uint32            `json:"gid"`
	Draft           Draft             `json:"draft"`
	DatabaseTargets map[string]string `json:"database_targets"`
	Configuration   []byte            `json:"configuration"`
}

// Diagnostics are emitted only through the bounded private stage stderr.
// Never forward arbitrary PostgreSQL Error(), SQL text, DSNs or credentials;
// only fixed categories and a strictly validated five-character SQLSTATE.
func addSiteStageErrorCategory(stage string, err error) (category, code string) {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && len(pg.Code) == 5 {
		safe := true
		for _, c := range pg.Code {
			if c < '0' || c > '9' {
				if c < 'A' || c > 'Z' {
					safe = false
					break
				}
			}
		}
		if safe {
			return "postgresql", pg.Code
		}
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout", "deadline-exceeded"
	case errors.Is(err, context.Canceled):
		return "interrupted", "context-canceled"
	case errors.Is(err, product.ErrBootstrapOperationStale):
		return "bootstrap", "stale-binding"
	case errors.Is(err, product.ErrBootstrapReconciliation):
		return "bootstrap", "reconciliation-required"
	case errors.Is(err, product.ErrBootstrapOperationState):
		return "bootstrap", "unsafe-state"
	case errors.Is(err, product.ErrBootstrapOperationInput):
		return "bootstrap", "invalid-input"
	default:
		return "stage", stage + "-failed"
	}
}

func RuntimeAddSiteStage(parent context.Context, args []string) error {
	if len(args) != 1 || os.Geteuid() == 0 {
		return ErrAuthority
	}
	stage := args[0]
	if stage != "prepare" && stage != "migrate" && stage != "bootstrap-site-b" && stage != "check" {
		return ErrInput
	}
	var task addSiteRuntimeTask
	b, err := io.ReadAll(io.LimitReader(os.Stdin, (3<<20)+1))
	if err != nil || len(b) > 3<<20 || decode(b, &task) != nil || !operationSyntax.MatchString(task.OperationID) ||
		task.UID != uint32(os.Geteuid()) || task.GID != uint32(os.Getegid()) ||
		!siteBRequested(task.Draft) || len(task.DatabaseTargets) != 3 || len(task.Configuration) == 0 {
		return ErrInput
	}
	account, err := user.Lookup("rixa")
	if err != nil || account.Uid != strconv.Itoa(os.Geteuid()) || account.Gid != strconv.Itoa(os.Getegid()) ||
		privateDir(runtimeRoot, os.Geteuid(), false) != nil {
		return ErrAuthority
	}
	c, err := product.ParseConfig(task.Configuration)
	if err != nil || len(c.Sites) != 2 || c.Sites[0].ID != "site-a" || c.Sites[1].ID != "site-b" ||
		c.Sites[1].Origin != task.Draft.SiteBOrigin {
		return ErrInput
	}
	ctx, cancel := context.WithTimeout(parent, 150*time.Second)
	defer cancel()
	identities, err := databaseIdentities(ctx, task.Draft)
	if runtimeDatabaseBindingError(identities, task.DatabaseTargets, err) != nil {
		return ErrState
	}
	receipt := stageReceipt{Stage: stage, Code: "verified"}
	switch stage {
	case "prepare":
		for _, path := range []string{"/var/lib/rixa/site-b", "/var/lib/rixa/site-b/media", "/var/lib/rixa/site-b/public"} {
			if err = privateDir(path, os.Geteuid(), true); err != nil {
				return ErrState
			}
		}
	case "migrate":
		err = product.MigrateAddedSite(ctx, c, databaseLookup(task.Draft))
	case "bootstrap-site-b":
		if task.Draft.SiteBPassword == "" {
			return ErrInput
		}
		var result product.BootstrapOperationResult
		result, err = product.ApplyBootstrapOperation(ctx, product.BootstrapOperationRequest{
			OperationID: task.OperationID + "-bootstrap-site-b", StatePath: runtimeRoot + "/bootstrap-site-b.json",
			Configuration: task.Configuration, Scope: "site:site-b",
			Login: task.Draft.SiteBLogin, Password: task.Draft.SiteBPassword,
		}, databaseLookup(task.Draft), slog.New(slog.NewJSONHandler(io.Discard, nil)))
		if err == nil && !principalSyntax.MatchString(result.Principal) {
			return ErrState
		}
		receipt.Principal = result.Principal
	case "check":
		resolved, resolveErr := c.ResolveRuntime(ctx, databaseLookup(task.Draft))
		err = resolveErr
		if err == nil {
			var runtime *product.Runtime
			runtime, err = product.BuildRuntime(resolved, slog.New(slog.NewJSONHandler(io.Discard, nil)))
			if err == nil {
				err = product.Check(ctx, runtime)
			}
		}
	}
	if err != nil {
		category, code := addSiteStageErrorCategory(stage, err)
		return fmt.Errorf("%w: add-site operation=%s stage=%s category=%s code=%s",
			ErrPrerequisite, task.OperationID, stage, category, code)
	}
	return json.NewEncoder(os.Stdout).Encode(receipt)
}

// addSiteChildStage runs in the same frozen manager image used by the root
// executor, but under the unprivileged rixa account and its private stdin.
func (m *Manager) addSiteChildStage(ctx context.Context, j *addSiteJournal, parent *journal, stage string, config []byte) (stageReceipt, error) {
	if j == nil || parent == nil || !operationSyntax.MatchString(j.Draft.OperationID) {
		return stageReceipt{}, ErrState
	}
	task := addSiteRuntimeTask{
		OperationID: j.Draft.OperationID, UID: parent.UID, GID: parent.GID,
		Draft: j.Draft.withInstallation(parent.Draft), DatabaseTargets: j.DatabaseTargets, Configuration: config,
	}
	data, err := json.Marshal(task)
	if err != nil || len(data) > 3<<20 {
		return stageReceipt{}, ErrState
	}
	path := filepath.Join(managerRoot, j.ManagerSHA256)
	hash, err := hashFile(path)
	if err != nil || hash != j.ManagerSHA256 || !trustedExecutable(path) {
		return stageReceipt{}, ErrManagerChanged
	}
	childCtx, cancel := context.WithTimeout(ctx, 165*time.Second)
	defer cancel()
	cmd := exec.CommandContext(childCtx, path, "manage", "internal-add-site-stage", stage)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{
		Uid: parent.UID, Gid: parent.GID, Groups: []uint32{parent.GID},
	}}
	cmd.Stdin = bytes.NewReader(data)
	cmd.Dir = "/var/lib/rixa"
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "HOME=/var/lib/rixa"}
	var out, diagnostic boundedOutput
	cmd.Stdout, cmd.Stderr = &out, &diagnostic
	err = cmd.Run()
	if err != nil || out.exceeded || diagnostic.exceeded {
		if diagnostic.Len() > 0 {
			copy := *parent
			copy.Draft.OperationID = j.Draft.OperationID
			copy.Draft.SiteBDSN = j.Draft.SiteBDSN
			copy.Draft.SiteBPassword = j.Draft.SiteBPassword
			m.retainDiagnostic(&copy, "add-site "+stage+": "+diagnostic.String())
		}
		return stageReceipt{}, errors.Join(ErrPrerequisite, err)
	}
	var result stageReceipt
	if decode(out.Bytes(), &result) != nil || result.Stage != stage || result.Code != "verified" {
		return stageReceipt{}, ErrState
	}
	if stage == "bootstrap-site-b" && !principalSyntax.MatchString(result.Principal) {
		return stageReceipt{}, ErrState
	}
	if stage != "bootstrap-site-b" && result.Principal != "" {
		return stageReceipt{}, ErrState
	}
	return result, nil
}

// Composition is always derived from the byte-pinned predecessor, never from
// defaults that could discard the site's existing configuration.
func addSiteConfigCandidate(config []byte, j *addSiteJournal, parent *journal) ([]byte, error) {
	if j == nil || parent == nil {
		return nil, ErrState
	}
	plan, err := composeAddSiteConfig(config, environmentFile(parent.Draft), parent, j.Draft.AddSiteInspectionDraft, j.SiteBPrincipal)
	if err != nil {
		return nil, err
	}
	parsed, err := product.ParseConfig(plan.Configuration)
	if err != nil || len(parsed.Sites) != 2 ||
		!reflect.DeepEqual(plan.Environment, environmentFile(j.Draft.withInstallation(parent.Draft))) {
		return nil, ErrState
	}
	return plan.Configuration, nil
}
