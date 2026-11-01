// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"
	"time"
)

func (m *Manager) updateHTTPPostflight(parent context.Context, j *journal) error {
	if err := parent.Err(); err != nil {
		return err
	}
	client, err := m.localClient(j)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	// Type=exec proves execve, not a listening HTTPS server. The entire existing
	// read-only proof shares one finite deadline, including all transport work.
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	for _, origin := range nativeSiteOrigins(j.Draft) {
		for _, probe := range []struct {
			path   string
			status int
		}{
			{"/admin/login", http.StatusOK}, {"/auth/csrf", http.StatusUnauthorized},
		} {
			var lastRefused error
			for {
				request, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+probe.path, nil)
				if err != nil {
					return err
				}
				request.Header.Set("Origin", origin)
				request.Header.Set("X-Identity-Request", "1")
				response, err := client.Do(request)
				if err == nil {
					count, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, (2<<20)+1))
					closeErr := response.Body.Close()
					if readErr != nil || closeErr != nil {
						return errors.Join(ErrPrerequisite, readErr, closeErr)
					}
					if count > 2<<20 {
						return fmt.Errorf("%w: native update HTTPS response exceeds its body bound", ErrPrerequisite)
					}
					if response.StatusCode != probe.status {
						return fmt.Errorf("%w: native update HTTPS probe %s%s returned HTTP %d, expected %d", ErrPrerequisite, origin, probe.path, response.StatusCode, probe.status)
					}
					break
				}
				if ctx.Err() != nil {
					return errors.Join(ctx.Err(), lastRefused, err)
				}
				// Only a positively identified refused TCP connection is evidence of this
				// startup race. TLS/certificate/permission/status/read failures fail now.
				var dial *net.OpError
				if !errors.As(err, &dial) || dial.Op != "dial" || dial.Net != "tcp" || !errors.Is(dial.Err, syscall.ECONNREFUSED) {
					return err
				}
				lastRefused = err
				timer := time.NewTimer(250 * time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					return errors.Join(ctx.Err(), lastRefused)
				case <-timer.C:
				}
			}
		}
	}
	return nil
}
