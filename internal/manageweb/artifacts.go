// SPDX-License-Identifier: MPL-2.0
package manageweb

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/AChWorks/rixa/internal/releaseartifact"
)

var releaseTag = regexp.MustCompile("^v[0-9]+\\.[0-9]+\\.[0-9]+([.-][0-9A-Za-z][0-9A-Za-z.-]*)?$")
var sourceCommit = regexp.MustCompile("^[0-9a-f]{40}$")
var stagingAttempt = regexp.MustCompile("^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$")

func (c *Console) artifacts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		c.fail(w, r, 405, "invalid_request")
		return
	}
	if _, e := c.AuthenticateRequest(r, true); e != nil {
		c.fail(w, r, 401, "access_required")
		return
	}
	if !c.effects.TryLock() {
		c.fail(w, r, 409, "busy")
		return
	}
	defer c.effects.Unlock()
	if e := c.gate(r.Context(), "configure"); e != nil {
		c.fail(w, r, 409, "action_unavailable")
		return
	}
	// Optional for direct clients; the browser supplies a fresh bounded ID.
	// Status echoes only a committed verified selection's ID, allowing exact
	// lost-ack observation without replaying an upload or download.
	attempt := r.Header.Get("X-Rixa-Artifact-Attempt")
	if len(r.Header.Values("X-Rixa-Artifact-Attempt")) > 1 || (attempt != "" && !stagingAttempt.MatchString(attempt)) {
		c.fail(w, r, 400, "invalid_request")
		return
	}
	c.mutex.Lock()
	if attempt != "" && c.artifactIDs[attempt] {
		c.mutex.Unlock()
		c.fail(w, r, 409, "action_unavailable")
		return
	}
	if c.artifactAttempts >= 3 {
		c.mutex.Unlock()
		c.fail(w, r, 429, "artifact_limit")
		return
	}
	c.artifactAttempts++
	c.mutex.Unlock()
	dir, e := os.MkdirTemp(c.stage, "artifact-")
	if e != nil {
		c.fail(w, r, 503, "unavailable")
		return
	}
	var a artifact
	switch r.URL.Path {
	case "/artifact/fetch":
		var in struct {
			Channel string `json:"channel"`
			Release string `json:"release"`
		}
		if decode(w, r, &in, 2048) != nil {
			c.fail(w, r, 400, "invalid_request")
			return
		}
		// Fetch itself requires a new output directory and owns the exact fixed
		// repository/channel/tag/provenance policy. No browser URL is accepted.
		out := filepath.Join(dir, "download")
		got, err := c.deps.fetch(r.Context(), releaseartifact.FetchRequest{OutputDir: out, Channel: in.Channel, ReleaseTag: in.Release})
		if err != nil || got.Status != "verified" || got.Verification == nil || got.Verification.Status != "verified" {
			c.fail(w, r, 503, "artifact_failed")
			return
		}
		if !releaseTag.MatchString(got.ReleaseTag) || !sourceCommit.MatchString(got.SourceCommit) || !strings.HasPrefix(got.ArchivePath, out+string(os.PathSeparator)) || !strings.HasPrefix(got.BundlePath, out+string(os.PathSeparator)) {
			c.fail(w, r, 503, "artifact_failed")
			return
		}
		a = artifact{got.ReleaseTag, got.SourceCommit, got.ArchivePath, got.BundlePath, got.Verification.SHA256}
	case "/artifact/upload":
		var err error
		a, err = c.upload(w, r, dir)
		if err != nil {
			c.fail(w, r, 400, "artifact_failed")
			return
		}
	default:
		c.fail(w, r, 404, "invalid_request")
		return
	}
	c.mutex.Lock()
	c.artifact = a
	c.artifactAttempt = attempt
	if attempt != "" {
		if c.artifactIDs == nil {
			c.artifactIDs = make(map[string]bool, 3)
		}
		c.artifactIDs[attempt] = true
	}
	c.mutex.Unlock()
	_, _, unsaved, _ := c.artifactState(r.Context())
	jsonResponse(w, 200, map[string]any{"artifact": publicArtifact{a.Release, a.Source, a.SHA256}, "artifact_attempt": attempt, "artifact_unsaved": unsaved, "message": text(c.localizer(r), "artifact_ready")})
}
func (c *Console) upload(w http.ResponseWriter, r *http.Request, dir string) (artifact, error) {
	fail := errors.New("unsupported release upload")
	kind, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || kind != "multipart/form-data" {
		return artifact{}, fail
	}
	r.Body = http.MaxBytesReader(w, r.Body, releaseartifact.MaxArchiveBytes+releaseartifact.MaxBundleBytes+(16<<10))
	defer r.Body.Close()
	reader, e := r.MultipartReader()
	if e != nil {
		return artifact{}, fail
	}
	values := map[string]string{}
	for i, name := range []string{"release", "source_commit", "archive", "bundle"} {
		part, e := reader.NextPart()
		if e != nil || part.FormName() != name {
			return artifact{}, fail
		}
		if i < 2 {
			b, e := io.ReadAll(io.LimitReader(part, 129))
			_ = part.Close()
			if e != nil || len(b) > 128 {
				return artifact{}, fail
			}
			values[name] = string(b)
			if i == 1 && (!releaseTag.MatchString(values["release"]) || !sourceCommit.MatchString(values["source_commit"])) {
				return artifact{}, fail
			}
		} else {
			path := filepath.Join(dir, "rixa-"+values["release"]+"-linux-amd64.tar.gz")
			maximum := releaseartifact.MaxArchiveBytes
			if name == "bundle" {
				path += ".sigstore.json"
				maximum = releaseartifact.MaxBundleBytes
			}
			f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if e != nil {
				return artifact{}, fail
			}
			n, copyErr := io.Copy(f, io.LimitReader(part, maximum+1))
			syncErr := f.Sync()
			closeErr := f.Close()
			_ = part.Close()
			if copyErr != nil || syncErr != nil || closeErr != nil || n == 0 || n > maximum {
				return artifact{}, fail
			}
			values[name] = path
		}
	}
	if _, e = reader.NextPart(); e != io.EOF {
		return artifact{}, fail
	}
	result, e := c.deps.verify(r.Context(), releaseartifact.Request{ArchivePath: values["archive"], BundlePath: values["bundle"], ReleaseTag: values["release"], SourceCommit: values["source_commit"]})
	if e != nil || result.Status != "verified" {
		return artifact{}, fail
	}
	return artifact{values["release"], values["source_commit"], values["archive"], values["bundle"], result.SHA256}, nil
}
