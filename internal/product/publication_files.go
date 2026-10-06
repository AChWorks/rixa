// SPDX-License-Identifier: MPL-2.0

package product

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const maxPublicationManifestBytes = 4 << 20

func (s *PublicationService) Ready() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := inspectMediaRoot(s.siteID+":public", s.root); err != nil {
		return ErrEditorialUnavailable
	}
	if err := syncDir(s.root); err != nil {
		return ErrEditorialUnavailable
	}
	generationsDir := filepath.Join(s.root, "generations")
	if err := os.MkdirAll(generationsDir, 0o700); err != nil {
		return ErrEditorialUnavailable
	}
	info, err := os.Lstat(generationsDir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return ErrEditorialUnavailable
	}

	currentPath := filepath.Join(s.root, "current")
	currentInfo, statErr := os.Lstat(currentPath)
	if errors.Is(statErr, os.ErrNotExist) {
		s.state.Store(newPublicationReadState(nil))
		s.cleanupStartup(nil)
		s.ready.Store(true)
		return nil
	}
	if statErr != nil || !currentInfo.Mode().IsRegular() || currentInfo.Mode().Perm() != 0o600 || currentInfo.Size() < 1 || currentInfo.Size() > 64 {
		return ErrEditorialUnavailable
	}
	if resolved, e := filepath.EvalSymlinks(currentPath); e != nil || resolved != currentPath {
		return ErrEditorialUnavailable
	}
	pointer, err := os.ReadFile(currentPath)
	if err != nil || len(pointer) == 0 || len(pointer) > 64 {
		return ErrEditorialUnavailable
	}
	currentID := strings.TrimSuffix(string(pointer), "\n")
	if currentID == "" || string(pointer) != currentID+"\n" || !validEditorialID(currentID) {
		return ErrEditorialUnavailable
	}

	generations := make([]*publishedGeneration, 0, publicationHistoryLimit)
	seen := make(map[string]struct{}, publicationHistoryLimit)
	next := currentID
	for len(generations) < publicationHistoryLimit && next != "" {
		if _, duplicate := seen[next]; duplicate {
			return ErrEditorialUnavailable
		}
		seen[next] = struct{}{}
		generation, loadErr := s.loadGeneration(next)
		if loadErr != nil {
			if len(generations) == 0 {
				return loadErr
			}
			if errors.Is(loadErr, os.ErrNotExist) {
				break
			}
			return loadErr
		}
		generations = append(generations, generation)
		next = generation.manifest.ParentGeneration
	}
	s.state.Store(newPublicationReadState(generations))
	s.cleanupStartup(seen)
	s.ready.Store(true)
	return nil
}

func writePublicationManifest(stage string, manifest *publicationManifest) error {
	if manifest == nil {
		return ErrEditorialUnavailable
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil || len(data) == 0 || len(data) > maxPublicationManifestBytes {
		return ErrEditorialLimited
	}
	data = append(data, '\n')
	path := filepath.Join(stage, "manifest.json")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return ErrEditorialUnavailable
	}
	n, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil || n != len(data) {
		_ = os.Remove(path)
		return ErrEditorialUnavailable
	}
	return nil
}

func (s *PublicationService) loadGeneration(id string) (*publishedGeneration, error) {
	if !validEditorialID(id) {
		return nil, ErrEditorialUnavailable
	}
	root := filepath.Join(s.root, "generations", id)
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		return nil, ErrEditorialUnavailable
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != root {
		return nil, ErrEditorialUnavailable
	}
	manifestPath := filepath.Join(root, "manifest.json")
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil || !manifestInfo.Mode().IsRegular() || manifestInfo.Mode().Perm() != 0o600 ||
		manifestInfo.Size() < 1 || manifestInfo.Size() > maxPublicationManifestBytes {
		return nil, ErrEditorialUnavailable
	}
	if resolved, e := filepath.EvalSymlinks(manifestPath); e != nil || resolved != manifestPath {
		return nil, ErrEditorialUnavailable
	}
	file, err := os.Open(manifestPath)
	if err != nil {
		return nil, ErrEditorialUnavailable
	}
	defer file.Close()
	limited := io.LimitReader(file, maxPublicationManifestBytes+1)
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	var manifest publicationManifest
	if err = decoder.Decode(&manifest); err != nil {
		return nil, ErrEditorialUnavailable
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, ErrEditorialUnavailable
	}
	if err = s.validateManifest(root, &manifest); err != nil {
		return nil, err
	}
	return &publishedGeneration{manifest: &manifest, root: root}, nil
}

func (s *PublicationService) validateManifest(root string, manifest *publicationManifest) error {
	if manifest == nil || manifest.Version != publicationManifestVersion ||
		manifest.SiteID != s.siteID || manifest.Origin != s.origin ||
		!validEditorialID(manifest.Generation) ||
		filepath.Base(root) != manifest.Generation ||
		manifest.CreatedAt.IsZero() || !validSHA256(manifest.SourceFingerprint) ||
		manifest.AppearanceRevision < 1 {
		return ErrEditorialUnavailable
	}
	if manifest.ParentGeneration != "" &&
		(!validEditorialID(manifest.ParentGeneration) || manifest.ParentGeneration == manifest.Generation) {
		return ErrEditorialUnavailable
	}
	if err := manifest.Policy.validate(); err != nil {
		return ErrEditorialUnavailable
	}
	if !validOperationID(manifest.Operation.ID) ||
		manifest.Operation.Generation != manifest.Generation ||
		!validSHA256(manifest.Operation.RequestHash) ||
		manifest.Operation.CreatedAt.IsZero() {
		return ErrEditorialUnavailable
	}
	if manifest.Operation.ContentID != "" && !validEditorialID(manifest.Operation.ContentID) {
		return ErrEditorialUnavailable
	}
	if manifest.Operation.Route != "" {
		normalized, err := normalizePublicationRoute(manifest.Operation.Route)
		if err != nil || normalized != manifest.Operation.Route {
			return ErrEditorialUnavailable
		}
	}
	publicDir := filepath.Join(root, "public")
	info, err := os.Lstat(publicDir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return ErrEditorialUnavailable
	}
	if resolved, e := filepath.EvalSymlinks(publicDir); e != nil || resolved != publicDir {
		return ErrEditorialUnavailable
	}

	for key, meta := range manifest.Files {
		if key != meta.Path || !safeGeneratedFilePath(key) ||
			meta.Size < 0 || !validSHA256(meta.SHA256) ||
			meta.ContentType == "" || meta.Cache == "" {
			return ErrEditorialUnavailable
		}
		path := filepath.Join(publicDir, filepath.FromSlash(key))
		info, err = os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != meta.Size {
			return ErrEditorialUnavailable
		}
		resolved, e := filepath.EvalSymlinks(path)
		if e != nil || resolved != path {
			return ErrEditorialUnavailable
		}
		if err = verifyFileSHA256(path, meta.SHA256); err != nil {
			return ErrEditorialUnavailable
		}
	}

	for id, asset := range manifest.Assets {
		if id != asset.SourceAssetID || !validEditorialID(id) ||
			asset.SourceRevision < 1 || !validSHA256(asset.SourceSHA256) ||
			!validSHA256(asset.SHA256) || asset.Size < 1 ||
			asset.Width < 1 || asset.Height < 1 ||
			(asset.MIME != "image/png" && asset.MIME != "image/jpeg") ||
			asset.Profile == "" || !strings.HasPrefix(asset.PublicPath, "/assets/") ||
			asset.File == "" || asset.PublicPath != "/"+filepath.ToSlash(asset.File) {
			return ErrEditorialUnavailable
		}
		meta, ok := manifest.Files[asset.File]
		if !ok || meta.SHA256 != asset.SHA256 || meta.Size != asset.Size || meta.ContentType != asset.MIME {
			return ErrEditorialUnavailable
		}
	}

	for id, entry := range manifest.Entries {
		if id != entry.ContentID || !validEditorialID(id) || !validContentKind(entry.Kind) {
			return ErrEditorialUnavailable
		}
		normalized, e := normalizePublicationRoute(entry.DesiredRoute)
		if e != nil || normalized != entry.DesiredRoute {
			return ErrEditorialUnavailable
		}
		for _, alias := range entry.Aliases {
			normalized, e = normalizePublicationRoute(alias)
			if e != nil || normalized != alias || alias == entry.DesiredRoute {
				return ErrEditorialUnavailable
			}
		}
		if entry.Active {
			if entry.Revision < 1 || entry.FirstPublishedAt.IsZero() || entry.ModifiedAt.IsZero() ||
				!validSHA256(entry.SourceKey) || !safeGeneratedFilePath(entry.File) {
				return ErrEditorialUnavailable
			}
			if entry.CanonicalRoute != "/" {
				normalized, e = normalizePublicationRoute(entry.CanonicalRoute)
				if e != nil || normalized != entry.CanonicalRoute {
					return ErrEditorialUnavailable
				}
			}
			if _, ok := manifest.Files[entry.File]; !ok {
				return ErrEditorialUnavailable
			}
		} else if entry.CanonicalRoute != "" || entry.Revision != 0 || entry.File != "" {
			return ErrEditorialUnavailable
		}
	}

	for route, target := range manifest.Routes {
		if !safeManifestRoute(route) {
			return ErrEditorialUnavailable
		}
		switch target.Kind {
		case "file":
			if !safeGeneratedFilePath(target.File) {
				return ErrEditorialUnavailable
			}
			if _, ok := manifest.Files[target.File]; !ok {
				return ErrEditorialUnavailable
			}
		case "redirect":
			if target.Target == "" || !safeManifestRoute(target.Target) {
				return ErrEditorialUnavailable
			}
		case "gone":
			if target.ContentID == "" || !validEditorialID(target.ContentID) {
				return ErrEditorialUnavailable
			}
		default:
			return ErrEditorialUnavailable
		}
	}
	if _, ok := manifest.Routes["/"]; !ok {
		return ErrEditorialUnavailable
	}
	if _, ok := manifest.Routes["/sitemap.xml"]; !ok {
		return ErrEditorialUnavailable
	}
	if _, ok := manifest.Routes["/robots.txt"]; !ok {
		return ErrEditorialUnavailable
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func safeManifestRoute(value string) bool {
	switch value {
	case "/", "/sitemap.xml", "/robots.txt":
		return true
	}
	if strings.HasPrefix(value, "/assets/") {
		return false
	}
	normalized, err := normalizePublicationRoute(value)
	return err == nil && normalized == value
}

func (s *PublicationService) activateGeneration(generation string) (bool, error) {
	temp := filepath.Join(s.root, ".current-"+generation+".tmp")
	current := filepath.Join(s.root, "current")
	file, err := os.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return false, err
	}
	body := []byte(generation + "\n")
	n, writeErr := file.Write(body)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil || n != len(body) {
		_ = os.Remove(temp)
		return false, ErrEditorialUnavailable
	}
	if err = os.Rename(temp, current); err != nil {
		_ = os.Remove(temp)
		return false, err
	}
	if err = syncDir(s.root); err != nil {
		return true, err
	}
	return true, nil
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	err = dir.Sync()
	closeErr := dir.Close()
	return errors.Join(err, closeErr)
}

func (s *PublicationService) cleanupStartup(retained map[string]struct{}) {
	entries, err := os.ReadDir(s.root)
	if err == nil {
		for _, entry := range entries {
			name := entry.Name()
			if strings.HasPrefix(name, ".stage-") {
				id := strings.TrimPrefix(name, ".stage-")
				if validEditorialID(id) && entry.IsDir() {
					_ = os.RemoveAll(filepath.Join(s.root, name))
				}
			}
			if strings.HasPrefix(name, ".current-") && strings.HasSuffix(name, ".tmp") && !entry.IsDir() {
				id := strings.TrimSuffix(strings.TrimPrefix(name, ".current-"), ".tmp")
				if validEditorialID(id) {
					_ = os.Remove(filepath.Join(s.root, name))
				}
			}
		}
	}
	generationsDir := filepath.Join(s.root, "generations")
	entries, err = os.ReadDir(generationsDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !validEditorialID(entry.Name()) {
			continue
		}
		if retained != nil {
			if _, ok := retained[entry.Name()]; ok {
				continue
			}
		}
		_ = os.RemoveAll(filepath.Join(generationsDir, entry.Name()))
	}
}

func (s *PublicationService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s == nil || !s.ready.Load() {
		publicHTTPError(w, http.StatusServiceUnavailable)
		return
	}
	if r.TLS == nil || r.Host != s.authority || !safePublicRequest(r) {
		publicHTTPError(w, http.StatusNotFound)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		publicHTTPError(w, http.StatusMethodNotAllowed)
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		w.Header().Set("Retry-After", "1")
		publicHTTPError(w, http.StatusServiceUnavailable)
		return
	}

	s.readMu.Lock()
	state := s.state.Load()
	if state == nil || len(state.generations) == 0 {
		s.readMu.Unlock()
		publicHTTPError(w, http.StatusNotFound)
		return
	}
	active := state.generations[0]
	path := r.URL.Path
	if strings.HasPrefix(path, "/assets/") {
		generation := state.assets[path]
		if generation == nil {
			s.readMu.Unlock()
			publicHTTPError(w, http.StatusNotFound)
			return
		}
		for _, asset := range generation.manifest.Assets {
			if asset.PublicPath == path {
				generation.readers.Add(1)
				s.readMu.Unlock()
				s.servePublishedFile(w, r, generation, asset.File)
				return
			}
		}
		s.readMu.Unlock()
		publicHTTPError(w, http.StatusNotFound)
		return
	}

	route, ok := active.manifest.Routes[path]
	if !ok {
		s.readMu.Unlock()
		publicHTTPError(w, http.StatusNotFound)
		return
	}
	if route.Kind == "file" {
		active.readers.Add(1)
		s.readMu.Unlock()
		s.servePublishedFile(w, r, active, route.File)
		return
	}
	s.readMu.Unlock()

	switch route.Kind {
	case "redirect":
		setPublicSecurityHeaders(w)
		w.Header().Set("Cache-Control", generatedFileCache)
		w.Header().Set("X-Robots-Tag", "noindex, follow")
		http.Redirect(w, r, route.Target, http.StatusPermanentRedirect)
	case "gone":
		setPublicSecurityHeaders(w)
		w.Header().Set("Cache-Control", generatedFileCache)
		w.Header().Set("X-Robots-Tag", "noindex, noarchive")
		w.WriteHeader(http.StatusGone)
	default:
		publicHTTPError(w, http.StatusServiceUnavailable)
	}
}

func safePublicRequest(r *http.Request) bool {
	if r == nil || r.URL == nil || r.URL.Path == "" || r.URL.Path[0] != '/' ||
		strings.Contains(r.URL.Path, "\\") || strings.Contains(r.URL.Path, "//") {
		return false
	}
	for _, part := range strings.Split(r.URL.Path, "/") {
		if part == "." || part == ".." {
			return false
		}
	}
	for _, char := range r.URL.Path {
		if char < 0x20 || char == 0x7f || forbiddenEditorialDirectionControl(char) {
			return false
		}
	}
	raw := strings.ToLower(r.URL.RawPath)
	for _, forbidden := range []string{"%2f", "%5c", "%00", "%2e"} {
		if strings.Contains(raw, forbidden) {
			return false
		}
	}
	return true
}

func (s *PublicationService) servePublishedFile(w http.ResponseWriter, r *http.Request, generation *publishedGeneration, relative string) {
	meta, ok := generation.manifest.Files[relative]
	if !ok {
		publicHTTPError(w, http.StatusServiceUnavailable)
		return
	}
	defer func() {
		if generation.readers.Add(-1) == 0 && generation.retired.Load() {
			_ = os.RemoveAll(generation.root)
		}
	}()

	path := filepath.Join(generation.root, "public", filepath.FromSlash(relative))
	file, err := os.Open(path)
	if err != nil {
		publicHTTPError(w, http.StatusServiceUnavailable)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != meta.Size {
		publicHTTPError(w, http.StatusServiceUnavailable)
		return
	}

	setPublicSecurityHeaders(w)
	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("Cache-Control", meta.Cache)
	w.Header().Set("ETag", "\"sha256-"+meta.SHA256+"\"")
	if meta.Robots != "" {
		w.Header().Set("X-Robots-Tag", meta.Robots)
	}
	http.ServeContent(w, r, filepath.Base(relative), generation.manifest.CreatedAt, file)
}

func setPublicSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
}

func publicHTTPError(w http.ResponseWriter, status int) {
	setPublicSecurityHeaders(w)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, noarchive")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	if status != http.StatusNoContent {
		_, _ = io.WriteString(w, http.StatusText(status)+"\n")
	}
}

func verifyFileSHA256(path, expected string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return err
	}
	if !bytes.Equal(hash.Sum(nil), mustDecodeSHA256(expected)) {
		return ErrEditorialUnavailable
	}
	return nil
}

func mustDecodeSHA256(value string) []byte {
	decoded, _ := hex.DecodeString(value)
	return decoded
}
