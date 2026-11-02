"""Run the bounded account-free corpus and retain every Go test event, even on failure."""
import json
import pathlib
import subprocess
import sys


def main():
    expected = {
        "rillway/internal/access": ["TestSourceACLBypassCorpus/" + group for group in
                                   ("policy", "boundaries", "seeded", "validation", "peer")],
        "rillway/internal/proxy": [
            f"TestSourceACLAdmissionWire/{protocol}/{peer}/allow-{str(allowed).lower()}"
            for protocol in ("http", "connect", "socks")
            for peer in ("wire", "synthetic-mapped", "synthetic-malformed")
            for allowed in (False, True) if peer != "synthetic-malformed" or not allowed
        ] + ["TestSourceACLAdmissionUpdateRace", "TestSourcePolicyPreParserSharedUpdateAndDisconnect",
             "TestApplyConnectionLimitsConcurrentCloseAndLimitRaces"],
        "rillway/internal/app": [f"TestSourceACLAdmissionManagement/proxy-allow-{allowed}/{endpoint}"
                                 for allowed in ("false", "true") for endpoint in ("admin", "pac", "proxy")],
    }
    required = {(pkg, name) for pkg, names in expected.items() for name in names}
    roots = {name.split("/")[0] for _, name in required}
    out = pathlib.Path("coverage/source-acl")
    out.mkdir(parents=True, exist_ok=True)
    command = ["go", "test", "-race", "-shuffle=on", "-count=1", "-timeout=90s", "-json",
               "-run", "^(" + "|".join(sorted(roots)) + ")$", *expected]
    with (out / "events.jsonl").open("w") as stream:
        try:
            code = subprocess.run(command, stdout=stream, stderr=subprocess.STDOUT, timeout=600).returncode
        except subprocess.TimeoutExpired:
            code = 124
    started, outcomes, errors = set(), {}, []
    for line in (out / "events.jsonl").read_text().splitlines():
        try:
            row = json.loads(line)
        except json.JSONDecodeError:
            errors.append("non-JSON output")
            continue
        if name := row.get("Test"):
            key = (row["Package"], name)
            if row["Action"] == "run":
                started.add(key)
            elif row["Action"] in ("pass", "fail", "skip"):
                outcomes[key] = row["Action"]
    missing = sorted(required - outcomes.keys())
    parents = {(p, "/".join(t.split("/")[:i])) for p, t in outcomes for i in range(1, len(t.split("/")))}
    leaves = {p: sum(pkg == p and (pkg, t) not in parents for pkg, t in outcomes) for p in expected}
    if list(leaves.values()) != [2553, 18, 6]:
        errors.append("reviewed case inventory changed")
    ok = (code == 0 and not errors and not missing and started == outcomes.keys()
          and all(action == "pass" for action in outcomes.values()))
    summary = {"command": command, "exit": code, "ok": ok, "missing": missing, "leaves": leaves,
               "errors": errors, "tests": [{"package": p, "test": t, "status": a}
                                            for (p, t), a in sorted(outcomes.items())]}
    (out / "results.json").write_text(json.dumps(summary, indent=2) + "\n")
    print(f"Source ACL corpus: {len(outcomes)} test nodes; complete={ok}; go exit={code}")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
