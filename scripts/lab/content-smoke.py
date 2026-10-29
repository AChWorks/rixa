#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Actual HTTPS lab content proof; private credentials/cookies never enter output."""
import argparse
import hashlib
from http.cookiejar import CookieJar
from html.parser import HTMLParser
import json
import os
from pathlib import Path
import ssl
import stat
import sys
from urllib.error import HTTPError
from urllib.request import build_opener, HTTPCookieProcessor, HTTPSHandler, ProxyHandler, Request
import uuid


def private_json(path):
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077 or info.st_size > 65536:
        raise ValueError("private bounded fixture JSON required")
    return json.loads(path.read_text())


def save(path, data, initial=False):
    target = path if initial else path.with_name(path.name + ".tmp-" + str(uuid.uuid4()))
    fd = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "w") as output:
        json.dump(data, output, indent=2)
        output.write("\n")
        output.flush()
        os.fsync(output.fileno())
    if not initial:
        os.replace(target, path)
    parent = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(parent)
    finally:
        os.close(parent)


class Client:
    def __init__(self, origin, ca):
        self.origin, self.csrf = origin, ""
        self.cookies = CookieJar()
        self.http = build_opener(ProxyHandler({}), HTTPCookieProcessor(self.cookies),
                                 HTTPSHandler(context=ssl.create_default_context(cafile=str(ca))))

    def request(self, method, path, body=None):
        data = None if body is None else json.dumps(body).encode()
        headers = {"Origin": self.origin, "X-Identity-Request": "1", "Sec-Fetch-Site": "same-origin"}
        if body is not None:
            headers["Content-Type"] = "application/json"
        if self.csrf:
            headers["X-CSRF-Token"] = self.csrf
        try:
            with self.http.open(Request(self.origin + path, data=data, headers=headers, method=method), timeout=25) as response:
                return response.read(2 << 20)
        except HTTPError as error:
            try:
                code = json.loads(error.read(65536)).get("code", "unavailable")
            except (ValueError, AttributeError):
                code = "unavailable"
            raise ValueError("HTTP " + str(error.code) + " " + method + " " + path + " code=" + str(code)[:80]) from None

    def post(self, path, data):
        response = self.request("POST", path, data)
        return json.loads(response) if response else {}

    def login(self, login, password):
        self.csrf = self.post("/auth/login", {"login": login, "password": password})["csrf"]

    def logout(self):
        if self.csrf:
            self.post("/auth/logout", {})
            self.csrf = ""


class Inputs(HTMLParser):
    def __init__(self):
        super().__init__()
        self.values = {}

    def handle_starttag(self, tag, attrs):
        values = dict(attrs)
        if tag == "input" and "name" in values:
            self.values.setdefault(values["name"], []).append(values.get("value", ""))


def proof(args):
    marker = Path("/etc/rixa-disposable-lab")
    if os.getuid() != 0 or not marker.is_file() or marker.read_text().strip() != "rixa.disposable-lab.v1":
        raise ValueError("this mutation proof requires the explicitly marked owned guest")
    info = args.receipt.parent.stat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
        raise ValueError("receipt parent must be a private owned directory")
    draft = private_json(args.draft)
    if draft.get("control_origin") != "https://control.rixa.test" or draft.get("site_origin") != "https://site.rixa.test":
        raise ValueError("this proof is restricted to the declared local laboratory HTTPS origins")
    if args.action == "publish" and args.receipt.exists():
        raise ValueError("prior content attempt exists; inspect its operation IDs, never recreate blindly")
    state = {"operations": {}, "state": "prepared"} if args.action == "publish" else private_json(args.receipt)
    if args.action == "check" and state.get("state") != "verified":
        raise ValueError("prior mutation is unresolved; inspect its retained operation IDs")
    control = Client(draft["control_origin"], args.ca)
    site = Client(draft["site_origin"], args.ca)
    if args.action == "publish":
        save(args.receipt, state, initial=True)
    try:
        control.login(draft["control_login"], draft["control_password"])
        assert b"<!doctype html" in control.request("GET", "/admin").lower()
        control.logout()
        site.login(draft["site_login"], draft["site_password"])
        assert b"<!doctype html" in site.request("GET", "/admin/content").lower()
        if args.action == "publish":
            def mutate(stage, path, body):
                operation = str(uuid.uuid4())
                state["operations"][stage] = operation
                state["state"] = stage + "_attempted"
                save(args.receipt, state)
                return site.post(path, dict(body, operation_id=operation))
            created = mutate("create", "/admin/content/create", {
                "kind": "post", "title": "Native guest original", "body_html": "<p>Private original native guest draft.</p>"})
            state["id"] = created["id"]
            state["revision"] = created["revision"]
            saved = mutate("save", "/admin/content/save", {
                "id": state["id"], "expected_head": state["revision"],
                "title": "Native guest published proof", "body_html": "<p>Retained native guest publication proof.</p>"})
            state["revision"] = saved["revision"]
            before_intent = Inputs()
            before_intent.feed(site.request("GET", "/admin/content/edit/" + state["id"]).decode())
            versions = set(before_intent.values.get("expected_publication_version", []))
            if len(versions) != 1:
                raise ValueError("actual edit form has no unambiguous intent version")
            mutate("intent", "/admin/content/publication-intent", {
                "id": state["id"], "expected_head": state["revision"],
                "expected_publication_version": versions.pop(), "revision": state["revision"]})
            edit = Inputs()
            edit.feed(site.request("GET", "/admin/content/edit/" + state["id"]).decode())
            generations = edit.values.get("expected_generation", [])
            if len(generations) != 1:
                raise ValueError("actual edit form has no unique publication generation")
            state["route"] = "/lab/native-proof/"
            published = mutate("publish", "/admin/content/publish", {
                "id": state["id"], "expected_generation": generations[0], "route": state["route"]})
            state["generation"] = published["generation"]
        preview = site.request("GET", "/admin/content/preview/" + state["id"] + "/" + state["revision"])
        assert b"Retained native guest publication proof." in preview
        public = Client(draft["site_origin"], args.ca).request("GET", state["route"])
        assert b"Native guest published proof" in public and b"Retained native guest publication proof." in public
        public_hash = hashlib.sha256(public).hexdigest()
        if args.action == "check":
            assert public_hash == state["public_sha256"], "public bytes changed after restart"
        else:
            state.update(state="verified", public_sha256=public_hash)
            save(args.receipt, state)
    finally:
        try:
            control.logout()
        finally:
            site.logout()
    print(json.dumps({"status": "verified", "action": args.action, "id": state["id"],
                      "revision": state["revision"], "generation": state["generation"],
                      "public_sha256": public_hash, "sessions": "revoked after each scope"}))


if __name__ == "__main__":
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("publish", "check"))
    parser.add_argument("--draft", type=Path, required=True)
    parser.add_argument("--ca", type=Path, required=True)
    parser.add_argument("--receipt", type=Path, required=True)
    try:
        proof(parser.parse_args())
    except Exception as error:
        print("content proof: " + str(error), file=sys.stderr)
        sys.exit(1)
