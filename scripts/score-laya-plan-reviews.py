#!/usr/bin/env python3
"""Score two blind plan reviews against an ATENEA Laya evaluation report."""

import argparse
import json
from collections import Counter, defaultdict
from pathlib import Path


CHOICES = {"a", "b", "tie", "both_bad"}


def read_reviews(path):
    rows = {}
    for line_number, line in enumerate(Path(path).read_text(encoding="utf-8").splitlines(), 1):
        if not line.strip():
            continue
        row = json.loads(line)
        case_id = row.get("id", "")
        choice = str(row.get("preferred_option", "")).strip().lower()
        if not case_id or case_id in rows or choice not in CHOICES or not str(row.get("reason", "")).strip():
            raise ValueError(f"{path}: invalid or duplicate review on line {line_number}")
        rows[case_id] = choice
    return rows


def score(report, first, second, adjudication=None):
    cases = {row["id"]: row for row in report["cases"]}
    if len(cases) != report["count"] or set(first) != set(cases) or set(second) != set(cases):
        raise ValueError("both reviewers must cover every report case exactly once")
    if adjudication is not None and not set(adjudication).issubset(cases):
        raise ValueError("adjudication contains an unknown case")
    by_split = defaultdict(Counter)
    unresolved = []
    for case_id, row in cases.items():
        metrics = by_split[row["split"]]
        metrics["count"] += 1
        if first[case_id] == second[case_id]:
            choice = first[case_id]
            metrics["agreement"] += 1
        else:
            metrics["disagreement"] += 1
            choice = adjudication.get(case_id) if adjudication else None
            if choice is None:
                unresolved.append(case_id)
                continue
            metrics["adjudicated"] += 1
        if choice in {"a", "b"}:
            metrics[row["blinded_" + choice]] += 1
        else:
            metrics[choice] += 1
    disagreements = {case_id for case_id in cases if first[case_id] != second[case_id]}
    if adjudication is not None and set(adjudication) != disagreements:
        raise ValueError("adjudication must cover exactly the reviewer disagreements")
    return {
        "evidence": "blind human plan preference; separate from intent accuracy and live workflow outcomes",
        "total": len(cases),
        "unresolved_count": len(unresolved),
        "unresolved_ids": unresolved,
        "by_split": {split: dict(counts) for split, counts in sorted(by_split.items())},
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--report", required=True, help="metrics report produced by atenea-laya-eval")
    parser.add_argument("--reviewer", action="append", required=True, help="completed private review packet; pass twice")
    parser.add_argument("--adjudication", help="completed packet with choices for disagreements only")
    args = parser.parse_args()
    if len(args.reviewer) != 2:
        parser.error("pass --reviewer exactly twice")
    report = json.loads(Path(args.report).read_text(encoding="utf-8"))
    try:
        adjudication = read_reviews(args.adjudication) if args.adjudication else None
        result = score(report, read_reviews(args.reviewer[0]), read_reviews(args.reviewer[1]), adjudication)
    except (KeyError, ValueError) as error:
        parser.error(str(error))
    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
