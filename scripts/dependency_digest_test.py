import datetime
import pathlib
import tempfile
import unittest
from unittest import mock

import dependency_digest as digest


class DigestTests(unittest.TestCase):
    def test_json_stream_and_replaced_modules(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            result = digest.collect(root, lambda _: '\n{"Main":true}\n{"Path":"example.org/lib","Version":"v1.0.0","Update":{"Version":"v1.1.0"}}\n{"Replace":{},"Update":{"Version":"v2"}}')
        self.assertEqual(result, [("Go", "example.org/lib", "v1.0.0", "v1.1.0")])

    def test_action_lookup_deduplicates_and_resolves_annotated_tag(self):
        sha = "a" * 40
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            (root / ".github/workflows").mkdir(parents=True)
            (root / ".github/workflows/ci.yml").write_text(f"uses: actions/test@{sha}\nuses: actions/test@{sha}\n")
            calls = []

            def run(args):
                calls.append(args)
                if args[0] == "go":
                    return "{}"
                if args[-1].endswith("latest"):
                    return '{"tag_name":"v2.0.0"}'
                if "/git/ref/" in args[-1]:
                    return '{"object":{"type":"tag","sha":"tag-object"}}'
                return '{"object":{"type":"commit","sha":"' + sha + '"}}'

            self.assertEqual(digest.collect(root, run), [])
            self.assertEqual(len(calls), 4)

    def test_render_sanitizes_upstream_metadata(self):
        body = digest.render([("Go", "<script>@all|\n", "v1", "v2")], "example/project", datetime.date(2026, 1, 1))
        self.assertNotIn("<script>", body)
        self.assertNotIn("@all|", body)
        self.assertIn("Checked: 2026-01-01", body)

    def test_workflow_publisher_reuses_only_bot_owned_digest(self):
        import json
        import os
        workflow = (pathlib.Path(__file__).parent.parent / ".github/workflows/dependency-digest.yml").read_text()
        code = workflow.split("        run: |\n", 1)[1]
        code = "\n".join(line[10:] for line in code.splitlines())
        issue = {"number": 9, "title": digest.TITLE, "body": digest.MARKER + "\nold", "author": {"login": "github-actions[bot]"}}
        for author, expected in [("github-actions[bot]", "PATCH"), ("someone", "POST")]:
            issue["author"]["login"] = author
            with mock.patch.dict(os.environ, {"GH_REPO": "example/project"}), mock.patch("pathlib.Path.read_text", return_value=digest.MARKER + "\nreport"), mock.patch("subprocess.check_output", return_value=json.dumps([issue])), mock.patch("subprocess.run") as run:
                exec(compile(code, "workflow-publisher", "exec"), {})
                self.assertEqual(run.call_args.args[0][3], expected)

    def test_invalid_artifact_is_never_published(self):
        import os
        workflow = (pathlib.Path(__file__).parent.parent / ".github/workflows/dependency-digest.yml").read_text()
        code = "\n".join(line[10:] for line in workflow.split("        run: |\n", 1)[1].splitlines())
        with mock.patch.dict(os.environ, {"GH_REPO": "example/project"}), mock.patch("pathlib.Path.read_text", return_value="untrusted report"), mock.patch("subprocess.run") as run:
            with self.assertRaises(SystemExit):
                exec(compile(code, "workflow-publisher", "exec"), {})
            run.assert_not_called()


if __name__ == "__main__":
    unittest.main()
