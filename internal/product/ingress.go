// SPDX-License-Identifier: MPL-2.0

package product

import (
	"errors"
	"net/http"

	"github.com/AChWorks/achrix"
	"github.com/AChWorks/achrix/multisite"
)

type ingressHandler struct {
	runtime          *Runtime
	controlAuthority string
	singleAuthority  string
	singleSiteID     string
}

func newIngressHandler(runtime *Runtime) http.Handler {
	control, _ := originAuthority(runtime.Config.Control.Origin)
	h := &ingressHandler{runtime: runtime, controlAuthority: control}
	if runtime.router == nil && len(runtime.Config.Sites) == 1 {
		authority, _ := originAuthority(runtime.Config.Sites[0].Origin)
		h.singleAuthority = authority
		h.singleSiteID = runtime.Config.Sites[0].ID
	}
	return h
}

func (h *ingressHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.TLS == nil || r.Host == "" {
		failIngress(w, http.StatusBadRequest)
		return
	}
	if r.Host == h.controlAuthority {
		h.runtime.Control.Handler.ServeHTTP(w, r)
		return
	}
	if h.runtime.router == nil {
		if r.Host != h.singleAuthority {
			failIngress(w, http.StatusNotFound)
			return
		}
		site := h.runtime.Sites[h.singleSiteID]
		if site == nil {
			failIngress(w, http.StatusNotFound)
			return
		}
		site.Handler.ServeHTTP(w, r)
		return
	}

	id, err := h.runtime.router.service.Resolve(r.Context(), routerPrincipal, r.Host)
	if err != nil {
		switch {
		case errors.Is(err, multisite.ErrNotFound), errors.Is(err, multisite.ErrInput), errors.Is(err, achrix.ErrDenied):
			failIngress(w, http.StatusNotFound)
		default:
			failIngress(w, http.StatusServiceUnavailable)
		}
		return
	}
	site := h.runtime.Sites[string(id)]
	if site == nil {
		failIngress(w, http.StatusNotFound)
		return
	}
	site.Handler.ServeHTTP(w, r)
}

func failIngress(w http.ResponseWriter, status int) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"code":"not_available"}`))
}
