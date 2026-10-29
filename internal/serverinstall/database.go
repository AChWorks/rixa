// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"time"

	"github.com/AChWorks/rixa/internal/product"
	"github.com/jackc/pgx/v5"
)

// Retain physical server/database identities, not just mutable DSN addresses.
// Product.DatabaseTargets first enforces its existing safe TLS/isolation rules.
func databaseIdentities(parent context.Context, d Draft) (map[string]string, error) {
	c, e := product.ParseConfig(configBytes(d, "", ""))
	if e != nil {
		return nil, ErrInput
	}
	targets, e := c.DatabaseTargets(parent, databaseLookup(d))
	if e != nil {
		return nil, ErrPrerequisite
	}
	identities := map[string]string{}
	for _, target := range targets {
		ctx, cancel := context.WithTimeout(parent, 3*time.Second)
		cfg, e := pgx.ParseConfig(target.DSN)
		if e != nil {
			cancel()
			return nil, ErrInput
		}
		cfg.ConnectTimeout = time.Second
		conn, e := pgx.ConnectConfig(ctx, cfg)
		if e != nil {
			cancel()
			return nil, ErrPrerequisite
		}
		var version int
		var server, database string
		e = conn.QueryRow(ctx, "SELECT current_setting('server_version_num')::int, c.system_identifier::text, d.oid::text FROM pg_control_system() AS c JOIN pg_database AS d ON d.datname=current_database()").Scan(&version, &server, &database)
		closeCtx, closeCancel := context.WithTimeout(context.WithoutCancel(parent), time.Second)
		ce := conn.Close(closeCtx)
		closeCancel()
		cancel()
		if e != nil || ce != nil || version/10000 != 18 || server == "" || database == "" {
			return nil, ErrPrerequisite
		}
		identities[target.ID] = server + ":" + database
	}
	if len(identities) != 2 {
		return nil, ErrInput
	}
	return identities, nil
}
