// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AChWorks/achrix"
)

const (
	themePreviewLifetime  = 10 * time.Minute
	themePreviewMaxActive = 3
	themePreviewCookie    = "rixa-theme-preview"
)

type themePreview struct {
	root              string
	manifest          *publicationManifest
	expires           time.Time
	themeRevision     int64
	sourceFingerprint string
}

type ThemePreviewReceipt struct {
	URL               string    `json:"url"`
	ThemeRevision     int64     `json:"theme_revision"`
	SourceFingerprint string    `json:"source_fingerprint"`
	ExpiresAt         time.Time `json:"expires_at"`
}

// Preview creates an immutable and short-lived static view of exactly one
// Theme revision with current published editorial content and transformed
// Media. It cannot publish or mutate the active public generation.
func (s *ThemeService) Preview(ctx context.Context, actor achrix.Principal, revision int64) (ThemePreviewReceipt, error) {
	if revision < 0 {
		return ThemePreviewReceipt{}, ErrEditorialInvalid
	}
	if err := s.authorize(ctx, actor, CapabilityThemePreview); err != nil {
		return ThemePreviewReceipt{}, err
	}
	if s.publication == nil || !s.publication.ready.Load() {
		return ThemePreviewReceipt{}, ErrEditorialUnavailable
	}

	s.previewMu.Lock()
	defer s.previewMu.Unlock()
	s.evictExpiredPreviews(time.Now())
	if len(s.previews) >= themePreviewMaxActive {
		return ThemePreviewReceipt{}, ErrEditorialLimited
	}

	bundle := defaultThemeBundle()
	if revision > 0 {
		source, err := s.loadRevisionTrusted(ctx, revision)
		if err != nil {
			return ThemePreviewReceipt{}, err
		}
		bundle = source.Bundle
	}
	sourceUnlock, err := s.editorial.acquirePublicationSource(ctx)
	if err != nil {
		return ThemePreviewReceipt{}, err
	}
	defer sourceUnlock()

	snapshot, err := s.editorial.store.publicationSnapshot(ctx)
	if err != nil {
		return ThemePreviewReceipt{}, err
	}
	root, err := os.MkdirTemp(s.publication.root, ".theme-preview-")
	if err != nil {
		return ThemePreviewReceipt{}, ErrEditorialUnavailable
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(root)
		}
	}()
	publicDir := filepath.Join(root, "public")
	if err = os.Mkdir(publicDir, 0o700); err != nil {
		return ThemePreviewReceipt{}, ErrEditorialUnavailable
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	entries, routes, err := buildPublicationLayout(snapshot, nil, PublicationRequest{}, now)
	if err != nil {
		return ThemePreviewReceipt{}, err
	}
	assets, assetBytes, err := s.publication.prepareAssets(ctx, actor, snapshot, publicDir)
	if err != nil {
		return ThemePreviewReceipt{}, err
	}
	manifest := &publicationManifest{
		Version: publicationManifestVersion, SiteID: s.publication.siteID, Origin: s.publication.origin,
		CreatedAt: now, Generation: randText(), SourceFingerprint: snapshot.Fingerprint,
		AppearanceRevision: snapshot.Appearance.Revision, ThemeRevision: revision,
		Policy: s.publication.policy, Entries: entries, Routes: routes, Assets: assets,
		Files: make(map[string]publicationFile),
	}
	rendered, err := renderPublication(publicDir, manifest, snapshot, assets, s.publication.catalog, &bundle)
	if err != nil {
		return ThemePreviewReceipt{}, err
	}
	if assetBytes+rendered > maxPublicationBytes {
		return ThemePreviewReceipt{}, ErrEditorialLimited
	}
	if err = s.publication.verifyAssets(ctx, actor, assets); err != nil {
		return ThemePreviewReceipt{}, err
	}
	ticket := randText()
	if !validEditorialID(ticket) {
		return ThemePreviewReceipt{}, ErrEditorialUnavailable
	}
	expiry := time.Now().UTC().Add(themePreviewLifetime)
	if s.previews == nil {
		s.previews = make(map[string]*themePreview)
	}
	s.previews[ticket] = &themePreview{root: root, manifest: manifest, expires: expiry, themeRevision: revision, sourceFingerprint: snapshot.Fingerprint}
	keep = true
	return ThemePreviewReceipt{
		URL:           s.previewOrigin + "/entry/" + ticket,
		ThemeRevision: revision, SourceFingerprint: snapshot.Fingerprint, ExpiresAt: expiry,
	}, nil
}

func (s *ThemeService) evictExpiredPreviews(now time.Time) {
	for key, item := range s.previews {
		if !now.Before(item.expires) {
			_ = os.RemoveAll(item.root)
			delete(s.previews, key)
		}
	}
}
func (s *ThemeService) cleanupPreviews() {
	if s == nil {
		return
	}
	s.previewMu.Lock()
	defer s.previewMu.Unlock()
	for key, item := range s.previews {
		_ = os.RemoveAll(item.root)
		delete(s.previews, key)
	}
}

// The browser preview ticket is deliberately NOT an Admin session. It grants
// only a time-limited read of a pre-rendered snapshot on the separate origin.
func (s *ThemeService) PreviewHandler() http.Handler {
	if !s.enabled() {
		return nil
	}
	authority, err := originAuthority(s.previewOrigin)
	if err != nil {
		return nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setPublicSecurityHeaders(w)
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Robots-Tag", "noindex, noarchive")
		if r.TLS == nil || r.Host != authority || !safePublicRequest(r) {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		ticket := ""
		entry := false
		if strings.HasPrefix(r.URL.Path, "/entry/") {
			ticket = strings.TrimPrefix(r.URL.Path, "/entry/")
			entry = true
		} else {
			cookie, e := r.Cookie(themePreviewCookie)
			if e == nil {
				ticket = cookie.Value
			}
		}
		if !validEditorialID(ticket) {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}

		s.previewMu.Lock()
		s.evictExpiredPreviews(time.Now())
		snapshot := s.previews[ticket]
		if snapshot == nil {
			s.previewMu.Unlock()
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		if entry {
			maxAge := int(time.Until(snapshot.expires).Seconds())
			if maxAge < 1 {
				maxAge = 1
			}
			http.SetCookie(w, &http.Cookie{
				Name: themePreviewCookie, Value: ticket, Path: "/",
				HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: maxAge,
			})
			s.previewMu.Unlock()
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}

		// Hold the read lock for file serving so expiry cleanup never removes
		// the validated stage while a page/asset is being sent.
		defer s.previewMu.Unlock()
		route, ok := snapshot.manifest.Routes[r.URL.Path]
		if !ok && strings.HasPrefix(r.URL.Path, "/assets/") {
			for _, asset := range snapshot.manifest.Assets {
				if asset.PublicPath == r.URL.Path {
					route = publicationRoute{Kind: "file", File: asset.File}
					ok = true
					break
				}
			}
		}
		if !ok {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		if route.Kind == "redirect" {
			if !strings.HasPrefix(route.Target, "/") {
				http.Error(w, "Not Found", http.StatusNotFound)
				return
			}
			http.Redirect(w, r, route.Target, http.StatusPermanentRedirect)
			return
		}
		if route.Kind != "file" {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		meta, exists := snapshot.manifest.Files[route.File]
		if !exists || !safeGeneratedFilePath(route.File) {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		file, err := os.Open(filepath.Join(snapshot.root, "public", filepath.FromSlash(route.File)))
		if err != nil {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() != meta.Size || info.Mode().Perm() != 0600 {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", meta.ContentType)
		if meta.CSP != "" {
			w.Header().Set("Content-Security-Policy", meta.CSP)
		}
		http.ServeContent(w, r, filepath.Base(route.File), snapshot.manifest.CreatedAt, file)
	})
}

// On restart, no in-memory token survives; all abandoned ephemeral previews
// are disposable, scoped to this site's private public root.
func cleanupStaleThemePreviews(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), ".theme-preview-") {
			continue
		}
		if err = os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return fmt.Errorf("%w: stale preview removal", ErrEditorialUnavailable)
		}
	}
	return nil
}
