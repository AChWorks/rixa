// SPDX-License-Identifier: MPL-2.0

package releaseartifact

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func openRegular(path string, maximum int64) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maximum {
		f.Close()
		return nil, ErrInput
	}
	return f, nil
}

// The local operator trusts its installed official verifier. Privileged callers
// may use only root-owned helpers/parents; PATH cannot select a runtime-user
// writable executable. Other users and writable non-sticky ancestors are rejected.
func findHelper() (string, error) {
	path, err := exec.LookPath("gh")
	if err != nil {
		return "", ErrHelper
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", ErrHelper
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", ErrHelper
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 || info.Mode().Perm()&0o111 == 0 || info.Mode().Perm()&0o022 != 0 || !trustedOwner(info) || !trustedParents(filepath.Dir(path)) {
		return "", ErrHelper
	}
	return path, nil
}
func trustedOwner(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && (st.Uid == 0 || st.Uid == uint32(os.Geteuid()))
}
func trustedParents(dir string) bool {
	for {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || !trustedOwner(info) {
			return false
		}
		if info.Mode().Perm()&0o022 != 0 {
			st, ok := info.Sys().(*syscall.Stat_t)
			// Root-owned sticky temporary directories preserve their owner's child.
			if !ok || st.Uid != 0 || info.Mode()&os.ModeSticky == 0 {
				return false
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return true
		}
		dir = parent
	}
}

var versionLine = regexp.MustCompile(`^gh version ([0-9]+)\.([0-9]+)\.([0-9]+)(?:\s|$)`)

func supportedVersion(data []byte) bool {
	match := versionLine.FindSubmatch(data)
	if len(match) != 4 {
		return false
	}
	major, e1 := strconv.Atoi(string(match[1]))
	minor, e2 := strconv.Atoi(string(match[2]))
	patch, e3 := strconv.Atoi(string(match[3]))
	minimum := strings.Split(MinimumGHVersion, ".")
	minimumMajor, _ := strconv.Atoi(minimum[0])
	minimumMinor, _ := strconv.Atoi(minimum[1])
	minimumPatch, _ := strconv.Atoi(minimum[2])
	return e1 == nil && e2 == nil && e3 == nil && major == minimumMajor && (minor > minimumMinor || minor == minimumMinor && patch >= minimumPatch)
}

func runHelper(ctx context.Context, helper string, args []string, dir string) ([]byte, string, error) {
	env, redactions := helperEnvironment(dir)
	version, diagnostic, err := helperCommand(ctx, helper, []string{"--version"}, env, dir)
	if err != nil || !supportedVersion(version) {
		found := ""
		if match := versionLine.FindSubmatch(version); len(match) == 4 {
			found = " Found " + string(match[1]) + "." + string(match[2]) + "." + string(match[3]) + "."
		}
		return nil, cleanDiagnostic("Official gh >= "+MinimumGHVersion+" is required."+found+" "+diagnostic, dir, redactions), ErrHelper
	}
	help, diagnostic, err := helperCommand(ctx, helper, []string{"attestation", "verify", "--help"}, env, dir)
	if err != nil {
		return nil, cleanDiagnostic(diagnostic, dir, redactions), ErrHelper
	}
	for _, flag := range []string{"--bundle", "--source-ref", "--source-digest", "--signer-digest", "--signer-workflow", "--deny-self-hosted-runners", "--cert-oidc-issuer", "--hostname", "--format"} {
		if !strings.Contains(string(help), flag) {
			return nil, "The installed gh lacks required attestation verification capabilities.", ErrHelper
		}
	}
	output, diagnostic, err := helperCommand(ctx, helper, args, env, dir)
	return output, cleanDiagnostic(diagnostic, dir, redactions), err
}

// No ambient token, gh configuration, custom Sigstore roots or interactive
// authentication enters the verifier. Standard transport CA/proxy settings remain
// usable; their actual credential values are redacted from diagnostic output.
func helperEnvironment(dir string) ([]string, []string) {
	env := []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "GH_CONFIG_DIR=" + filepath.Join(dir, "gh-config"), "XDG_CACHE_HOME=" + filepath.Join(dir, "cache"), "XDG_STATE_HOME=" + filepath.Join(dir, "state"), "XDG_DATA_HOME=" + filepath.Join(dir, "data"), "XDG_CONFIG_HOME=" + filepath.Join(dir, "config"), "GH_HOST=github.com", "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "GH_PAGER=cat", "PAGER=cat", "NO_COLOR=1", "CLICOLOR=0"}
	var redactions []string
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
			if u, err := url.Parse(value); err == nil && u.User != nil {
				redactions = append(redactions, u.User.String())
				if password, ok := u.User.Password(); ok && password != "" {
					redactions = append(redactions, password, url.QueryEscape(password), url.PathEscape(password))
				}
			}
		}
	}
	return env, redactions
}

type cappedWriter struct {
	data     []byte
	limit    int
	overflow bool
	cancel   context.CancelFunc
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	remaining := w.limit - len(w.data)
	if len(p) > remaining {
		w.data = append(w.data, p[:remaining]...)
		w.overflow = true
		w.cancel()
	} else {
		w.data = append(w.data, p...)
	}
	return len(p), nil
}
func helperCommand(parent context.Context, helper string, args, env []string, dir string) ([]byte, string, error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stdout := &cappedWriter{limit: maxOutputBytes, cancel: cancel}
	stderr := &cappedWriter{limit: maxDiagnosticBytes, cancel: cancel}
	command := exec.CommandContext(ctx, helper, args...)
	command.Env = env
	command.Dir = dir
	command.Stdin = nil
	command.Stdout, command.Stderr = stdout, stderr
	command.WaitDelay = time.Second
	err := command.Run()
	if stdout.overflow || stderr.overflow {
		return nil, "Verifier output exceeded the supported bound. Retained diagnostic prefix:\n" + string(stderr.data), ErrVerification
	}
	if err != nil && len(stderr.data) == 0 {
		return nil, "Verifier process failed or was unavailable.", err
	}
	return stdout.data, string(stderr.data), err
}
func cleanDiagnostic(detail, dir string, redactions []string) string {
	detail = strings.ReplaceAll(detail, dir, "<verification-input>")
	for _, secret := range redactions {
		if secret != "" {
			detail = strings.ReplaceAll(detail, secret, "[redacted]")
		}
	}
	// Retain meaningful public verifier context without terminal control codes.
	detail = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r >= 32 && r != 127 {
			return r
		}
		return -1
	}, detail)
	if len(detail) > maxDiagnosticBytes {
		detail = detail[:maxDiagnosticBytes]
	}
	return strings.TrimSpace(detail)
}
