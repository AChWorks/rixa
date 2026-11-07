// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/AChWorks/achrix"
	shell "github.com/AChWorks/achrix/admin"
	"github.com/AChWorks/achrix/media"
)

// A Control-origin Picker binds each request to a configured site and the
// current Control actor. It never uses Site Admin cookies or private Media bytes.
type controlPickerManager interface {
	AuthorizeSiteMedia(context.Context, achrix.Principal, string) error
	ListSiteMedia(context.Context, achrix.Principal, string, string, int) (media.Page, error)
	SelectSiteMedia(context.Context, achrix.Principal, string, string) (media.Selection, error)
}

type controlPickerAsset struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	MIME     string `json:"mime"`
	Size     int64  `json:"size"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Revision int64  `json:"revision,string"`
	State    string `json:"state"`
}

type controlPickerPage struct {
	Assets     []controlPickerAsset `json:"assets"`
	NextCursor string               `json:"next_cursor"`
}

var pickerCursorSyntax = regexp.MustCompile(`^[A-Za-z0-9_-]{36}$`)

var pickerMessageKeys = [...]string{
	"open", "results", "refresh", "next", "select", "loading",
	"ready", "empty", "stale", "denied", "unavailable",
}

func pickerAssetMetadata(a media.Selection) controlPickerAsset {
	return controlPickerAsset{
		ID: a.ID, Filename: a.Filename, MIME: a.MIME, Size: a.Size,
		Width: a.Width, Height: a.Height, Revision: a.Revision, State: a.State,
	}
}

func pickerReply(w http.ResponseWriter, view shell.Request, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		view.Diagnose("rixa", "control_picker_write", err)
		panic(http.ErrAbortHandler)
	}
}

func pickerFail(w http.ResponseWriter, view shell.Request, err error) {
	status, code := http.StatusServiceUnavailable, "unavailable"
	switch {
	case errors.Is(err, achrix.ErrDenied):
		status, code = http.StatusForbidden, "permission_denied"
	case errors.Is(err, media.ErrNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, media.ErrInput):
		status, code = http.StatusBadRequest, "invalid_input"
	case errors.Is(err, media.ErrLimited):
		status, code = http.StatusTooManyRequests, "limited"
	case errors.Is(err, media.ErrConflict):
		status, code = http.StatusConflict, "conflict"
	}
	if status >= http.StatusInternalServerError {
		view.Diagnose("rixa", "control_picker_request", err)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Code string `json:"code"`
	}{code})
}

// An authenticated Control Admin request is rooted at:
// /admin/sites/site/{siteID}/media/picker/{messages|page[/cursor]|asset/{id}}.
// AChrix Media v0.5.1 Picker mounts this endpoint with an explicit site root.
// The unchanged official script is provided separately on the Control origin.
func serveControlMediaPicker(manager controlPickerManager, w http.ResponseWriter, r *http.Request, view shell.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if manager == nil || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery || !strings.HasPrefix(r.URL.Path, "/site/") {
		pickerFail(w, view, media.ErrNotFound)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/site/"), "/")
	if (len(parts) != 4 && len(parts) != 5) || !siteIDSyntax.MatchString(parts[0]) || parts[1] != "media" || parts[2] != "picker" {
		pickerFail(w, view, media.ErrNotFound)
		return
	}
	siteID, action := parts[0], parts[3]
	if (action == "messages" && len(parts) != 4) ||
		(action == "page" && len(parts) == 5 && !pickerCursorSyntax.MatchString(parts[4])) ||
		(action == "asset" && (len(parts) != 5 || !validEditorialID(parts[4]))) ||
		(action != "messages" && action != "page" && action != "asset") {
		pickerFail(w, view, media.ErrNotFound)
		return
	}
	if err := manager.AuthorizeSiteMedia(r.Context(), view.Principal, siteID); err != nil {
		pickerFail(w, view, err)
		return
	}
	switch action {
	case "messages":
		if view.Localizer == nil {
			pickerFail(w, view, media.ErrUnavailable)
			return
		}
		messages := make(map[string]string, len(pickerMessageKeys))
		for _, key := range pickerMessageKeys {
			value, err := view.Localizer.Text("media", "picker."+key, nil)
			if err != nil {
				pickerFail(w, view, media.ErrUnavailable)
				return
			}
			messages[key] = value
		}
		pickerReply(w, view, struct {
			Locale   string            `json:"locale"`
			Messages map[string]string `json:"messages"`
		}{view.Language, messages})
	case "page":
		cursor := ""
		if len(parts) == 5 {
			cursor = parts[4]
		}
		page, err := manager.ListSiteMedia(r.Context(), view.Principal, siteID, cursor, 25)
		if err != nil {
			pickerFail(w, view, err)
			return
		}
		result := controlPickerPage{Assets: make([]controlPickerAsset, 0, len(page.Assets)), NextCursor: page.NextCursor}
		for _, a := range page.Assets {
			if a.State != "ready" {
				continue
			}
			// Recheck Select for every visible asset, including readiness and
			// current site-specific authorization. Errors other than denied /
			// genuinely vanished choices fail the whole page closed.
			selected, err := manager.SelectSiteMedia(r.Context(), view.Principal, siteID, a.ID)
			if errors.Is(err, achrix.ErrDenied) || errors.Is(err, media.ErrNotFound) || errors.Is(err, media.ErrConflict) {
				continue
			}
			if err != nil {
				pickerFail(w, view, err)
				return
			}
			if selected.State == "ready" && selected.Revision > 0 {
				result.Assets = append(result.Assets, pickerAssetMetadata(selected))
			}
		}
		// A Control grant can be revoked while the collection is being read.
		// Never return an already assembled page after that revocation.
		if err := manager.AuthorizeSiteMedia(r.Context(), view.Principal, siteID); err != nil {
			pickerFail(w, view, err)
			return
		}
		pickerReply(w, view, result)
	case "asset":
		a, err := manager.SelectSiteMedia(r.Context(), view.Principal, siteID, parts[4])
		if err != nil {
			pickerFail(w, view, err)
			return
		}
		if a.State != "ready" || a.Revision < 1 {
			pickerFail(w, view, media.ErrNotFound)
			return
		}
		if view.Localizer == nil {
			pickerFail(w, view, media.ErrUnavailable)
			return
		}
		message, err := view.Localizer.Text("media", "picker.selected", map[string]string{"Filename": a.Filename})
		if err != nil {
			pickerFail(w, view, media.ErrUnavailable)
			return
		}
		pickerReply(w, view, struct {
			Asset   controlPickerAsset `json:"asset"`
			Message string             `json:"message"`
		}{pickerAssetMetadata(a), message})
	}
}
