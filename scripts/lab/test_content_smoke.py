# SPDX-License-Identifier: MPL-2.0
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("content_smoke", Path(__file__).with_name("content-smoke.py"))
smoke = importlib.util.module_from_spec(spec)
spec.loader.exec_module(smoke)


class PreviewDenialTests(unittest.TestCase):
    def test_explicit_authentication_denials(self):
        for status in (401, 403):
            smoke.check_preview_denial(status, b'{"code":"unauthorized"}')

    def test_success_redirect_missing_and_server_errors_are_not_denial(self):
        for status in (200, 302, 303, 404, 500, 503):
            with self.subTest(status=status), self.assertRaises(ValueError):
                smoke.check_preview_denial(status, b"unavailable")

    def test_denial_cannot_include_private_content_or_unbounded_bytes(self):
        for body in (b"Retained native guest publication proof.", b"Native guest published proof", b"x" * 65537):
            with self.subTest(length=len(body)), self.assertRaises(ValueError):
                smoke.check_preview_denial(401, body)


if __name__ == "__main__":
    unittest.main()
