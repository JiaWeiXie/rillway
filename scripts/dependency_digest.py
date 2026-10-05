"""Collect update metadata without changing dependencies or opening PRs."""

import argparse
import datetime
import json
import pathlib
import re
import subprocess

MARKER = "<!-- rillway-dependency-digest -->"
TITLE = "Dependency update digest"
ACTION = re.compile(r"uses:\s*([\w.-]+/[\w.-]+)@([0-9a-f]{40})")


def command_json(args):
    result = subprocess.run(args, capture_output=True, text=True, timeout=300, check=True)
    return result.stdout


def json_stream(text):
    decoder = json.JSONDecoder()
    while text.strip():
        item, end = decoder.raw_decode(text.lstrip())
        yield item
        text = text.lstrip()[end:]


def cell(value):
    # Metadata is data, never Markdown, HTML, mentions or shell syntax.
    return re.sub(r"[^a-zA-Z0-9 /._+:-]", "?", str(value))[:240]


def direct_modules(run=command_json):
    """Return module requirements Rillway directly declares in go.mod.

    Upstream modules can declare their own development tools and packages. They
    belong in the complete Go module graph but cannot safely be upgraded on
    their own. The monthly digest follows only Rillway's direct requirements;
    dependency vulnerability checks still cover the full build list.
    """
    module = json.loads(run(["go", "mod", "edit", "-json"]))
    return {requirement["Path"] for requirement in module["Require"] if not requirement.get("Indirect")}


def collect(root, run=command_json, modules=None):
    updates = []
    if modules is None:
        modules = direct_modules(run)
    for module in json_stream(run(["go", "list", "-mod=readonly", "-m", "-u", "-json", "all"])):
        if module.get("Error"):
            raise ValueError("Module update lookup failed")
        if module.get("Update") and module.get("Path") in modules and not module.get("Main") and "Replace" not in module:
            updates.append(("Go", module["Path"], module["Version"], module["Update"]["Version"]))
    actions = set()
    for workflow in sorted((root / ".github/workflows").glob("*.yml")):
        actions.update(ACTION.findall(workflow.read_text()))
    for repo, current in sorted(actions):
        release = json.loads(run(["gh", "api", f"repos/{repo}/releases/latest"]))
        tag = release["tag_name"]
        ref = json.loads(run(["gh", "api", f"repos/{repo}/git/ref/tags/{tag}"]))["object"]
        for _ in range(5):
            if ref["type"] == "commit":
                break
            if ref["type"] != "tag":
                raise ValueError("Unexpected action tag target")
            ref = json.loads(run(["gh", "api", f"repos/{repo}/git/tags/{ref['sha']}"]))["object"]
        if ref["type"] != "commit":
            raise ValueError("Action tag nesting exceeded limit")
        if current != ref["sha"]:
            updates.append(("GitHub Actions", repo, current[:12], tag))
    return sorted(updates)


def render(updates, repository, date):
    if not re.fullmatch(r"[\w.-]+/[\w.-]+", repository):
        raise ValueError("Invalid repository")
    body = [MARKER, "# Dependency update digest", "", f"Checked: {date.isoformat()} (UTC)", "",
            "This issue is refreshed monthly. No dependencies are changed automatically.", "",
            "It reports Rillway's direct Go modules and pinned GitHub Actions; upstream transitive and development-only modules are excluded.", "",
            "Available versions need review and CI before installation; an update is not proof of a vulnerability.", ""]
    if updates:
        body += ["| Ecosystem | Dependency | Current | Available |", "| --- | --- | --- | --- |"]
        body += ["| " + " | ".join(cell(value) for value in row) + " |" for row in updates]
    else:
        body += ["No newer versions were found by this check."]
    body += ["", f"Review [Dependabot security alerts](https://github.com/{repository}/security/dependabot) separately.",
             "Security alerts remain enabled; automatic version/security PRs are disabled.", ""]
    return "\n".join(body)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=pathlib.Path, required=True)
    parser.add_argument("--repository", required=True)
    args = parser.parse_args()
    report = render(collect(pathlib.Path.cwd()), args.repository, datetime.datetime.now(datetime.timezone.utc).date())
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(report)


if __name__ == "__main__":
    main()
