#!/usr/bin/env python3
"""Opt-in tests for a freshly installed, disposable Ubuntu machine.

Run as root with RILLWAY_RUNTIME_ACCEPTANCE=1 and create an empty
/var/lib/rillway/disposable-test.marker first. Never run on a production host.
Only synthetic requests are sent. Credentials and configuration stay local.
"""

import copy
import json
import os
import pathlib
import socket
import ssl
import subprocess
import time
import unittest
import urllib.error
import urllib.request


CONFIG = pathlib.Path("/etc/rillway/config.json")


def load():
    return json.loads(CONFIG.read_text())


def write_config(config):
    temporary = CONFIG.with_suffix(".acceptance.tmp")
    temporary.write_text(json.dumps(config))
    temporary.chmod(0o600)
    stat = CONFIG.stat()
    os.chown(temporary, stat.st_uid, stat.st_gid)
    temporary.replace(CONFIG)


def api(path, body=None, method=None, authenticated=True, origin=None):
    config = load()
    headers = {"Content-Type": "application/json"}
    if authenticated:
        token = pathlib.Path(config["security"]["admin_token_file"]).read_text().strip()
        headers["Authorization"] = "Bearer " + token
    if origin:
        headers["Origin"] = origin
    context = ssl.create_default_context(cafile=config["security"]["tls_cert_file"])
    opener = urllib.request.build_opener(
        urllib.request.ProxyHandler({}), urllib.request.HTTPSHandler(context=context)
    )
    request = urllib.request.Request(
        "https://" + config["listeners"]["admin"] + "/api/v1/" + path,
        data=json.dumps(body).encode() if body is not None else None,
        headers=headers,
        method=method,
    )
    try:
        with opener.open(request, timeout=20) as response:
            return response.status, json.load(response)
    except urllib.error.HTTPError as error:
        return error.code, json.load(error)


def ready(predicate=lambda config: True):
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        try:
            status, config = api("config")
            if status == 200 and predicate(config):
                return config
        except (OSError, urllib.error.URLError):
            pass
        time.sleep(0.25)
    raise AssertionError("Management API did not become ready")


def curl(arguments):
    # Never return stderr (upstream diagnostics may contain sensitive data).
    result = subprocess.run(
        ["curl", "--fail", "--silent", "--show-error", "--max-time", "15"] + arguments,
        capture_output=True,
        timeout=20,
    )
    if result.returncode:
        raise AssertionError("curl request failed with exit " + str(result.returncode))
    return result.stdout


class RuntimeAcceptance(unittest.TestCase):
    def test_01_installation_and_default_config_path(self):
        config = ready()
        state = pathlib.Path(config["security"]["admin_token_file"]).parent
        for path, mode in [(CONFIG, 0o600), (CONFIG.parent, 0o700), (state, 0o700)]:
            self.assertEqual(path.stat().st_mode & 0o777, mode)
        for field in ["admin_token_file", "tls_cert_file", "tls_key_file"]:
            self.assertEqual(pathlib.Path(config["security"][field]).stat().st_mode & 0o777, 0o600)
        user = subprocess.check_output(["systemctl", "show", "rillway", "-p", "User", "--value"]).strip()
        self.assertEqual(user, b"rillway")
        self.assertEqual(os.readlink("/usr/local/bin/rillway"), "/usr/local/lib/rillway/rillway")
        rendered = subprocess.check_output(["rillway", "pac"], timeout=15)
        fetched = curl(["--noproxy", "*", "http://" + config["listeners"]["pac"] + "/proxy.pac"])
        self.assertEqual(rendered, fetched)

    def test_02_http_connect_and_socks5(self):
        config = ready()
        pac = "http://" + config["listeners"]["pac"] + "/proxy.pac"
        direct = curl(["--noproxy", "*", pac])
        self.assertEqual(curl(["--noproxy", "", "--proxy", "http://" + config["listeners"]["http"], pac]), direct)
        self.assertEqual(curl(["--noproxy", "", "--socks5-hostname", config["listeners"]["socks5"], pac]), direct)
        target = "https://example.com/"
        direct = curl(["--noproxy", "*", target])
        self.assertEqual(curl(["--noproxy", "", "--proxy", "http://" + config["listeners"]["http"], target]), direct)
        self.assertEqual(curl(["--noproxy", "", "--socks5-hostname", config["listeners"]["socks5"], target]), direct)

    def test_03_authentication_origin_validation_and_conflicts(self):
        self.assertEqual(api("config", authenticated=False)[0], 401)
        status, config = api("config")
        self.assertEqual(status, 200)
        invalid = copy.deepcopy(config)
        invalid["default_outbound"] = "missing"
        self.assertEqual(api("config", invalid, "PUT")[0], 422)
        self.assertEqual(api("config")[1]["revision"], config["revision"])
        self.assertEqual(api("config", config, "PUT", origin="https://untrusted.invalid")[0], 403)
        self.assertEqual(api("outbounds/direct", {"revision": config["revision"]}, "DELETE")[0], 422)
        changed = copy.deepcopy(config)
        entries = changed["pac"]["bypass_domains"]
        existing = next((entry for entry in entries if entry["value"] == "acceptance.test"), None)
        if existing:
            existing["enabled"] = not existing["enabled"]
        else:
            entries.append({"value": "acceptance.test", "enabled": True, "note": "Disposable test"})
        self.assertEqual(api("config", changed, "PUT")[0], 200)
        self.assertEqual(api("config", config, "PUT")[0], 409)

    def test_04_web_restart_rejects_invalid_saved_settings(self):
        original = ready()
        bad = copy.deepcopy(original)
        bad["default_outbound"] = "missing"
        try:
            write_config(bad)
            self.assertEqual(api("service/restart", {}, "POST")[0], 422)
        finally:
            write_config(original)
        self.assertEqual(ready()["revision"], original["revision"])
        host = original["listeners"]["http"].rsplit(":", 1)[0]
        with socket.socket() as occupied:
            occupied.bind((host, 0))
            occupied.listen()
            bad = copy.deepcopy(original)
            bad["listeners"]["http"] = host + ":" + str(occupied.getsockname()[1])
            try:
                write_config(bad)
                self.assertEqual(api("service/restart", {}, "POST")[0], 422)
            finally:
                write_config(original)
        self.assertEqual(ready()["listeners"], original["listeners"])

    def test_05_web_restart_loads_new_listener_without_replacing_process(self):
        original = ready()
        pid = subprocess.check_output(["systemctl", "show", "rillway", "-p", "MainPID", "--value"])
        host = original["listeners"]["http"].rsplit(":", 1)[0]
        with socket.socket() as temporary:
            temporary.bind((host, 0))
            new_address = host + ":" + str(temporary.getsockname()[1])
        changed = copy.deepcopy(original)
        changed["listeners"]["http"] = new_address
        changed["pac"]["proxy_address"] = new_address
        changed["revision"] += 1
        try:
            write_config(changed)
            self.assertEqual(api("service/restart", {}, "POST")[0], 202)
            ready(lambda config: config["listeners"]["http"] == new_address)
            pac = "http://" + original["listeners"]["pac"] + "/proxy.pac"
            self.assertIn(new_address.encode(), curl(["--noproxy", "", "--proxy", "http://" + new_address, pac]))
            with self.assertRaises(OSError):
                socket.create_connection(tuple([host, int(original["listeners"]["http"].rsplit(":", 1)[1])]), timeout=2)
            self.assertEqual(subprocess.check_output(["systemctl", "show", "rillway", "-p", "MainPID", "--value"]), pid)
        finally:
            write_config(original)
            self.assertEqual(api("service/restart", {}, "POST")[0], 202)
            ready(lambda config: config["listeners"] == original["listeners"])

    def test_06_systemd_stop_start_restart(self):
        for action in ["stop", "start", "restart"]:
            subprocess.run(["rillway", "service", action], check=True, capture_output=True, timeout=30)
            active = subprocess.run(["systemctl", "is-active", "--quiet", "rillway"]).returncode == 0
            self.assertEqual(active, action != "stop")
            if active:
                ready()

    def test_07_observations_have_real_transfer_totals(self):
        config = ready()
        curl(["--noproxy", "", "--proxy", "http://" + config["listeners"]["http"], "https://example.com/"])
        status, stats = api("stats")
        self.assertEqual(status, 200)
        self.assertGreater(stats["totals"]["download_bytes"], 0)
        flows = [flow for flow in stats["flows"] if flow["host"] == "example.com"]
        self.assertTrue(flows)
        self.assertEqual(flows[-1]["outbound"], "direct")
        self.assertTrue(flows[-1].get("ip"))
        self.assertNotIn("payload", json.dumps(stats))


if __name__ == "__main__":
    if os.environ.get("RILLWAY_RUNTIME_ACCEPTANCE") != "1" or os.geteuid() != 0:
        raise SystemExit("Requires root and RILLWAY_RUNTIME_ACCEPTANCE=1 on a disposable VM")
    if not pathlib.Path("/var/lib/rillway/disposable-test.marker").is_file():
        raise SystemExit("Disposable test marker missing; refusing to modify service/configuration")
    release = pathlib.Path("/etc/os-release").read_text()
    if 'ID=ubuntu' not in release or not any('VERSION_ID="' + version + '"' in release for version in ["24.04", "26.04"]):
        raise SystemExit("Requires Ubuntu 24.04/26.04")
    unittest.main(verbosity=2)
