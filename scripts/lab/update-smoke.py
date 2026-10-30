#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Guest-only retained native update proof; all credentials and cookies stay private."""
import argparse
import hashlib
import importlib.util
from http.cookiejar import Cookie
import json
import os
from pathlib import Path
import re
import secrets
import stat
import subprocess
import sys
import struct
import zlib
from urllib.error import HTTPError
from urllib.parse import urlsplit, unquote
from urllib.request import Request

spec = importlib.util.spec_from_file_location("content_smoke", Path(__file__).with_name("content-smoke.py"))
common = importlib.util.module_from_spec(spec)
spec.loader.exec_module(common)
private_json, save, Client, Inputs = common.private_json, common.save, common.Client, common.Inputs
def png_fixture():
    def chunk(kind, data):
        value = kind + data
        return struct.pack("!I", len(data)) + value + struct.pack("!I", zlib.crc32(value) & 0xffffffff)
    pixels = (b"\x00" + b"\x28\x70\xb0" * 32) * 32
    return (b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack("!IIBBBBB", 32, 32, 8, 2, 0, 0, 0))
            + chunk(b"IDAT", zlib.compress(pixels)) + chunk(b"IEND", b""))


PNG = png_fixture()


def guard(parent):
    marker = Path("/etc/rixa-disposable-lab")
    info = parent.lstat()
    if (os.getuid() != 0 or not marker.is_file()
            or marker.read_text().strip() != "rixa.disposable-lab.v1"
            or not stat.S_ISDIR(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o077):
        raise ValueError("explicitly marked guest and private root-owned receipt directory required")


def digest(data):
    return hashlib.sha256(data).hexdigest()


def cookie_state(client):
    fields = ("version", "name", "value", "port", "port_specified", "domain",
              "domain_specified", "domain_initial_dot", "path", "path_specified",
              "secure", "expires", "discard", "comment", "comment_url", "rfc2109")
    return {"csrf": client.csrf, "cookies": [dict({name: getattr(c, name) for name in fields},
            rest=dict(c._rest)) for c in client.cookies]}


def retained_client(origin, ca, state):
    client = Client(origin, ca)
    client.csrf = state["csrf"]
    for value in state["cookies"]:
        client.cookies.set_cookie(Cookie(**value))
    return client


def attempt(receipt, state, name, action):
    # Record the attempt before *every* mutating request, including login/upload
    # routes without a caller operation ID. A lost acknowledgement is inspected.
    import uuid
    operation = str(uuid.uuid4())
    state["attempts"][name] = {"id": operation, "status": "attempted"}
    state["state"] = name + "-attempted"
    save(receipt, state)
    result = action(operation)
    state["attempts"][name]["status"] = "acknowledged"
    save(receipt, state)
    return result


def upload(client, data):
    boundary = "rixa-lab-" + secrets.token_hex(20)
    body = (("--" + boundary + '\r\nContent-Disposition: form-data; name="file"; filename="retained.png"\r\n'
             "Content-Type: image/png\r\n\r\n").encode() + data + ("\r\n--" + boundary + "--\r\n").encode())
    request = Request(client.origin + "/admin/media/upload", data=body, method="POST",
                      headers={"Origin": client.origin, "X-Identity-Request": "1",
                               "Sec-Fetch-Site": "same-origin", "X-CSRF-Token": client.csrf,
                               "Content-Type": "multipart/form-data; boundary=" + boundary})
    try:
        with client.http.open(request, timeout=25) as response:
            return json.loads(response.read(65536))["asset"]["id"]
    except HTTPError as error:
        raise ValueError("Media upload HTTP " + str(error.code) + "; inspect private attempt") from None


def version(client, path, field):
    parsed = Inputs()
    parsed.feed(client.request("GET", path).decode())
    values = set(parsed.values.get(field, []))
    if len(values) != 1:
        raise ValueError("actual form lacks unambiguous " + field)
    return values.pop()


def expect_denied(client, path, body, expected):
    try:
        client.post(path, body)
    except ValueError as error:
        match = re.match(r"HTTP ([0-9]{3}) ", str(error))
        if match and int(match[1]) == expected:
            return
        raise ValueError("negative authorization check did not return expected denial") from None
    raise ValueError("negative authorization check was accepted")


def seed(args, draft):
    if args.receipt.exists():
        raise ValueError("prior seed attempt exists; inspect it, never repeat blindly")
    state = {"schema": "rixa.native-update-proof.v1", "state": "prepared", "attempts": {}}
    save(args.receipt, state, initial=True)
    control, site = Client(draft["control_origin"], args.ca), Client(draft["site_origin"], args.ca)
    attempt(args.receipt, state, "control-login", lambda _: control.login(draft["control_login"], draft["control_password"]))
    state["control_session"] = cookie_state(control)
    save(args.receipt, state)
    attempt(args.receipt, state, "site-login", lambda _: site.login(draft["site_login"], draft["site_password"]))
    state["site_session"] = cookie_state(site)
    save(args.receipt, state)
    state["member_password"] = secrets.token_urlsafe(28)
    save(args.receipt, state)
    attempt(args.receipt, state, "account", lambda _: control.post("/admin/sites/account-create",
            {"site_id": "site-a", "login": "retained-member", "password": state["member_password"]}))
    member = Client(draft["site_origin"], args.ca)
    attempt(args.receipt, state, "member-login", lambda _: member.login("retained-member", state["member_password"]))
    state["member_session"] = cookie_state(member)
    save(args.receipt, state)
    state["asset"] = attempt(args.receipt, state, "media", lambda _: upload(site, PNG))
    save(args.receipt, state)
    title = "Retained native فارسی"
    body = '<p>Retained native guest publication proof. فارسی English</p><figure data-media-id="' + state["asset"] + '"><figcaption>اصل تصویر</figcaption></figure>'
    created = attempt(args.receipt, state, "create", lambda operation: site.post("/admin/content/create",
            {"operation_id": operation, "kind": "post", "title": title, "body_html": body}))
    state.update(id=created["id"], revision=created["revision"])
    save(args.receipt, state)
    saved = attempt(args.receipt, state, "save", lambda operation: site.post("/admin/content/save",
            {"operation_id": operation, "id": state["id"], "expected_head": state["revision"],
             "title": title, "body_html": body + "<p>Revision two.</p>"}))
    state["revision"] = saved["revision"]
    save(args.receipt, state)
    edit = "/admin/content/edit/" + state["id"]
    intent_version = version(site, edit, "expected_publication_version")
    attempt(args.receipt, state, "intent", lambda operation: site.post("/admin/content/publication-intent",
            {"operation_id": operation, "id": state["id"], "expected_head": state["revision"],
             "expected_publication_version": intent_version, "revision": state["revision"]}))
    head = version(site, "/admin/appearance", "expected_head")
    attempt(args.receipt, state, "appearance", lambda operation: site.post("/admin/appearance/save",
            {"operation_id": operation, "expected_head": head, "site_title": "Retained native site",
             "site_description": "Retained فارسی", "site_language": "fa", "home_mode": "latest_posts",
             "home_page_id": "", "header_show_title": True, "header_tagline": "Retained header",
             "footer_text": "Retained footer", "theme": "dark"}))
    generation = version(site, edit, "expected_generation")
    state["route"] = "/lab/retained-native/"
    published = attempt(args.receipt, state, "publish", lambda operation: site.post("/admin/content/publish",
            {"operation_id": operation, "id": state["id"], "expected_generation": generation, "route": state["route"]}))
    state["generation"] = published["generation"]
    save(args.receipt, state)
    revoked = Client(draft["site_origin"], args.ca)
    attempt(args.receipt, state, "negative-login", lambda _: revoked.login(draft["site_login"], draft["site_password"]))
    state["revoked_session"] = cookie_state(revoked)
    save(args.receipt, state)
    attempt(args.receipt, state, "negative-logout", lambda _: revoked.logout())
    state["public_sha256"] = digest(Client(draft["site_origin"], args.ca).request("GET", state["route"]))
    public = Client(draft["site_origin"], args.ca).request("GET", state["route"])
    paths = set(re.findall(rb'/assets/[a-f0-9]{64}\.png', public))
    if len(paths) != 1 or title.encode() not in public:
        raise ValueError("published content/Media path missing")
    state["public_asset_path"] = paths.pop().decode()
    state["public_asset_sha256"] = digest(Client(draft["site_origin"], args.ca).request("GET", state["public_asset_path"]))
    state["private_asset_sha256"] = digest(site.request("GET", "/admin/media/read/" + state["asset"]))
    if state["private_asset_sha256"] != digest(PNG):
        raise ValueError("uploaded original bytes differ")
    state["state"] = "seeded"
    save(args.receipt, state)
    check(args, draft, state)
    return state


def check(args, draft, state):
    if (state.get("state") not in ("seeded", "verified")
            or not state.get("attempts")
            or any(value.get("status") != "acknowledged" for value in state["attempts"].values())):
        raise ValueError("seed mutation unresolved; inspect retained attempt IDs")
    site = retained_client(draft["site_origin"], args.ca, state["site_session"])
    control = retained_client(draft["control_origin"], args.ca, state["control_session"])
    revoked = retained_client(draft["site_origin"], args.ca, state["revoked_session"])
    assert b'/admin/sites/account-create' in control.request("GET", "/admin/sites/")
    assert b"<!doctype html" in site.request("GET", "/admin/content").lower()
    preview = "/admin/content/preview/" + state["id"] + "/" + state["revision"]
    assert b"Retained native guest publication proof." in site.request("GET", preview)
    common.anonymous_preview_denial(draft["site_origin"], args.ca, preview)
    public = Client(draft["site_origin"], args.ca)
    assert digest(public.request("GET", state["route"])) == state["public_sha256"], "public bytes changed"
    assert digest(public.request("GET", state["public_asset_path"])) == state["public_asset_sha256"], "public Media bytes changed"
    assert digest(site.request("GET", "/admin/media/read/" + state["asset"])) == state["private_asset_sha256"], "private Media bytes changed"
    # Empty invalid mutation bodies fail authentication before validation; no
    # operation is created. Never use a successful write for a retention check.
    expect_denied(revoked, "/admin/content/create", {}, 401)
    cross_state = json.loads(json.dumps(state["site_session"]))
    for cookie in cross_state["cookies"]:
        cookie["domain"] = urlsplit(draft["control_origin"]).hostname
    cross = retained_client(draft["control_origin"], args.ca, cross_state)
    expect_denied(cross, "/admin/sites/account-create", {}, 401)
    bad_csrf = retained_client(draft["site_origin"], args.ca, state["site_session"])
    bad_csrf.csrf = "x" * 43
    expect_denied(bad_csrf, "/admin/content/create", {}, 401)
    member = retained_client(draft["site_origin"], args.ca, state["member_session"])
    expect_denied(member, "/admin/content/create", {}, 403)
    if state["state"] != "verified":
        state["state"] = "verified"
        save(args.receipt, state)
    return state


def psql(dsn, sql):
    parsed = urlsplit(dsn)
    if parsed.scheme not in ("postgres", "postgresql") or parsed.hostname != "127.0.0.1":
        raise ValueError("explicit local guest PostgreSQL URL required")
    env = {"PATH": "/usr/bin:/bin", "LANG": "C.UTF-8", "PGHOST": parsed.hostname,
           "PGPORT": str(parsed.port or 5432), "PGUSER": unquote(parsed.username or ""),
           "PGPASSWORD": unquote(parsed.password or ""), "PGDATABASE": unquote(parsed.path.lstrip("/")),
           "PGSSLMODE": "disable", "PGCONNECT_TIMEOUT": "10", "PGOPTIONS": "-c default_transaction_read_only=on"}
    result = subprocess.run(["/usr/bin/psql", "-X", "-A", "-t", "--set=ON_ERROR_STOP=1", "-c", sql],
                            env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=25)
    if result.returncode != 0:
        raise ValueError("private PostgreSQL snapshot failed; no credentials emitted")
    if len(result.stdout) > 32 << 20:
        raise ValueError("snapshot row bound exceeded")
    return result.stdout


def file_snapshot(roots):
    result = {}
    for root in roots:
        if not root.exists() or root.is_symlink():
            raise ValueError("retained required root missing or linked")
        paths = [root] if root.is_file() else [root] + sorted(root.rglob("*"))
        for path in paths:
            info = path.lstat()
            if stat.S_ISLNK(info.st_mode) or not (stat.S_ISREG(info.st_mode) or stat.S_ISDIR(info.st_mode)):
                raise ValueError("retained scope contains unsupported special file")
            result[str(path)] = {"mode": stat.S_IMODE(info.st_mode), "uid": info.st_uid, "gid": info.st_gid,
                                 "kind": "file" if stat.S_ISREG(info.st_mode) else "directory"}
            if stat.S_ISREG(info.st_mode):
                result[str(path)]["sha256"] = digest(path.read_bytes())
    return result


def snapshot(args, draft):
    if args.receipt.exists():
        raise ValueError("snapshot receipt already exists")
    state = {"schema": "rixa.native-retained-snapshot.v1", "targets": {}, "tables": {}}
    for scope in ("control", "site"):
        dsn = draft[scope + "_dsn"]
        state["targets"][scope] = json.loads(psql(dsn, """SELECT json_build_object(
            'system_identifier',(SELECT system_identifier::text FROM pg_control_system()),
            'database_oid',(SELECT oid::text FROM pg_database WHERE datname=current_database()),
            'server_version_num',current_setting('server_version_num'))"""))
        inventory = psql(dsn, """SELECT schemaname || '.' || tablename FROM pg_tables
            WHERE schemaname NOT IN ('pg_catalog','information_schema') AND schemaname NOT LIKE 'pg_toast%'
            ORDER BY schemaname,tablename""").decode().splitlines()
        for table in inventory:
            if not re.fullmatch(r"[a-z_][a-z0-9_]*\.[a-z_][a-z0-9_]*", table):
                raise ValueError("unsupported snapshot table identifier")
            quoted = ".".join('"' + part + '"' for part in table.split("."))
            rows = psql(dsn, "SELECT row_to_json(t)::text FROM " + quoted + " t ORDER BY row_to_json(t)::text")
            state["tables"][scope + "/" + table] = {"rows": len(rows.splitlines()), "sha256": digest(rows)}
    if state["targets"]["control"]["database_oid"] == state["targets"]["site"]["database_oid"]:
        raise ValueError("control/site physical scope is not independent")
    state["files"] = file_snapshot([Path("/etc/rixa"), Path("/var/lib/rixa")])
    save(args.receipt, state, initial=True)
    return state


def differences(before, after):
    return {key: {"before": before.get(key), "after": after.get(key)}
            for key in sorted(set(before) | set(after)) if before.get(key) != after.get(key)}


FAILURE_RECEIPT = None

def main():
    global FAILURE_RECEIPT
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("seed", "check", "snapshot", "compare"))
    parser.add_argument("--draft", type=Path)
    parser.add_argument("--ca", type=Path)
    parser.add_argument("--receipt", type=Path, required=True)
    parser.add_argument("--before", type=Path)
    args = parser.parse_args()
    guard(args.receipt.parent)
    FAILURE_RECEIPT = args.receipt.with_name(args.receipt.name + ".failure.json")
    if args.action == "compare":
        before, after = private_json(args.before), private_json(args.receipt)
        delta = {key: differences(before[key], after[key]) for key in ("targets", "tables", "files")}
        print(json.dumps({"status": "identical" if not any(delta.values()) else "changed", "differences": delta}))
        return 0 if not any(delta.values()) else 1
    draft = private_json(args.draft)
    if (draft.get("control_origin") != "https://control.rixa.test"
            or draft.get("site_origin") != "https://site.rixa.test"):
        raise ValueError("only declared local guest origins are allowed")
    if args.action == "snapshot":
        state = snapshot(args, draft)
        print(json.dumps({"status": "captured", "tables": len(state["tables"]), "files": len(state["files"]),
                          "targets": state["targets"]}))
    else:
        state = seed(args, draft) if args.action == "seed" else check(args, draft, private_json(args.receipt))
        print(json.dumps({"status": "verified", "action": args.action, "content_id": state["id"],
                          "revision": state["revision"], "generation": state["generation"],
                          "sessions": "retained valid and revoked controls; tokens remain private",
                          "public_sha256": state["public_sha256"], "private_media_sha256": state["private_asset_sha256"]}))
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception as error:
        if FAILURE_RECEIPT is not None and not FAILURE_RECEIPT.exists():
            try:
                save(FAILURE_RECEIPT, {"error_type": type(error).__name__, "detail": str(error)[:8192]}, initial=True)
            except Exception:
                pass
        # Do not print arbitrary transport/SQL errors: they may contain secrets.
        print("native update proof failed (" + type(error).__name__ + "); inspect private fixture/state.", file=sys.stderr)
        sys.exit(1)
