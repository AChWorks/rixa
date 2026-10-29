#!/usr/bin/env bash
# SPDX-License-Identifier: MPL-2.0
# LAB FIXTURE ONLY: never invoked by the product installer.
set -euo pipefail
umask 077
[[ "$(id -u)" == 0 && "$(cat /etc/rixa-disposable-lab 2>/dev/null)" == rixa.disposable-lab.v1 ]] || {
  echo "This fixture requires the explicitly marked disposable guest." >&2; exit 1;
}
[[ "$(systemd-detect-virt)" == qemu && "$(dpkg --print-architecture)" == amd64 ]] || {
  echo "This fixture requires the owned QEMU amd64 guest." >&2; exit 1;
}
[[ "$(cat /etc/debian_version)" == 13.* ]] || { echo "Debian13 required." >&2; exit 1; }
cd /root/lab
[[ "$(stat -c '%u:%a' .)" == 0:700 && -f pgdg.asc && ! -L pgdg.asc && -f databases.sql && ! -L databases.sql ]] || {
  echo "Root-private fixture inputs are unavailable." >&2; exit 1;
}
[[ ! -e postgres-fixture.attempted && ! -e postgres-fixture.done ]] || {
  echo "A prior fixture attempt exists; inspect actual guest/DB state before any new effect." >&2; exit 1;
}
# Reviewed official PGDG certificate, fingerprint B97B0AFCAA1A47F044F244A07FCC7D46ACCC4CF8.
printf '%s\n' '0144068502a1eddd2a0280ede10ef607d1ec592ce819940991203941564e8e76  pgdg.asc' | sha256sum --check --status
: > postgres-fixture.attempted
install -m 0644 pgdg.asc /usr/share/keyrings/rixa-lab-pgdg.asc
cat > /etc/apt/sources.list.d/rixa-lab-pgdg.sources <<'EOF'
Types: deb
URIs: https://apt.postgresql.org/pub/repos/apt
Suites: trixie-pgdg
Architectures: amd64
Components: main
Signed-By: /usr/share/keyrings/rixa-lab-pgdg.asc
EOF
apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends postgresql-18
systemctl is-active --quiet postgresql@18-main
version="$(runuser -u postgres -- psql -X -Atqc 'SHOW server_version_num' postgres)"
[[ "$version" =~ ^18[0-9]{4}$ ]] || { echo "Unexpected PostgreSQL server version." >&2; exit 1; }
if ! runuser -u postgres -- psql -X --set=ON_ERROR_STOP=1 --dbname=postgres < databases.sql > postgres-fixture-sql.log 2>&1; then
  echo "Database fixture failed; inspect root-private postgres-fixture-sql.log before recovery." >&2
  exit 1
fi
runuser -u postgres -- psql -X -Atqc "SELECT count(*) FROM pg_database WHERE datname IN ('rixa_lab_control','rixa_lab_site')" postgres | {
  read -r count; [[ "$count" == 2 ]];
}
printf '%s\n' '127.0.0.1 control.rixa.test site.rixa.test' >> /etc/hosts
printf '%s\n' "postgresql_version_num=$version" > postgres-fixture.done
echo "Guest-only PG18 fixture ready; two separate databases/roles. This does not prove product provisioning."
