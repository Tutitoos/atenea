#!/usr/bin/env python3
"""Compare a Laya /v1/systemone service with ATENEA's labeled intent corpus."""

import argparse
import ipaddress
import json
import os
import sys
from pathlib import Path
from urllib.error import HTTPError, URLError
from urllib.parse import urlparse
from urllib.request import HTTPRedirectHandler, Request, build_opener


ROOT = Path(__file__).resolve().parents[1]
CORPUS = ROOT / "internal/decision/testdata/intent-evaluation.jsonl"
CRITERIA = {
    "understand": "Explain, summarize, or understand existing information without asking to search a specific target or make a plan.",
    "search": "Find, locate, or inspect existing information, code, or behavior.",
    "plan": "Design, plan, or recommend future work without asking to implement it now.",
    "change": "Implement, add, edit, fix, or refactor something now.",
}


class NoRedirectHandler(HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, new_url):
        return None


def load_corpus():
    return [json.loads(line) for line in CORPUS.read_text(encoding="utf-8").splitlines() if line.strip()]


def predict(endpoint, text, timeout, api_key, requested_model):
    payload = {
        "state": {"body": text},
        "questions": {
            "intent": {
                "type": "choice",
                "instructions": "Which intent best describes the action explicitly requested by the user? Distinguish planning a change from carrying it out.",
                "criteria": CRITERIA,
            }
        },
    }
    if requested_model:
        payload["model"] = requested_model
    body = json.dumps(payload).encode("utf-8")
    headers = {"Content-Type": "application/json"}
    if api_key:
        headers["Authorization"] = "Bearer " + api_key
    request = Request(endpoint, data=body, headers=headers, method="POST")
    try:
        with build_opener(NoRedirectHandler()).open(request, timeout=timeout) as response:
            raw = response.read((1 << 20) + 1)
        if len(raw) > (1 << 20):
            raise RuntimeError("Laya response exceeds 1 MiB")
        payload = json.loads(raw)
    except HTTPError as error:
        raise RuntimeError("Laya returned HTTP %d" % error.code) from None
    except (URLError, TimeoutError) as error:
        raise RuntimeError("Laya request failed: %s" % getattr(error, "reason", error)) from None
    except ValueError:
        raise RuntimeError("Laya returned invalid JSON") from None
    try:
        answer = payload["answers"]["intent"]
        intent = answer["choice"]
        confidence = float(answer["answer_confidence"])
        model = str(payload.get("model", ""))
        routing_model = str(payload.get("routing", {}).get("model", ""))
    except (KeyError, TypeError, ValueError) as error:
        raise RuntimeError("Laya response is missing a typed intent answer: %s" % error) from None
    if intent not in CRITERIA or not 0 <= confidence <= 1:
        raise RuntimeError("Laya response has an unsupported intent or confidence value")
    return intent, confidence, model, routing_model


def accuracy(rows, predicted):
    return sum(row["expected"] == value for row, value in zip(rows, predicted)) / len(rows) if rows else None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("endpoint", help="Laya endpoint, e.g. http://127.0.0.1:8000/v1/systemone")
    parser.add_argument("--threshold", type=float, default=0.8, help="answer_confidence threshold for Laya (default: 0.8)")
    parser.add_argument("--timeout", type=float, default=30, help="per-request timeout in seconds")
    parser.add_argument("--api-key-env", default="", help="environment variable holding the optional bearer token")
    parser.add_argument("--model", default="", help="optional Laya checkpoint/model ID to pin for this evaluation")
    args = parser.parse_args()
    parsed = urlparse(args.endpoint)
    if (
        parsed.scheme not in ("http", "https")
        or not parsed.hostname
        or parsed.path != "/v1/systemone"
        or parsed.query
        or parsed.fragment
        or parsed.username is not None
        or parsed.password is not None
    ):
        parser.error("endpoint must be an HTTP(S) URL ending in /v1/systemone, without credentials, query or fragment")
    if parsed.scheme == "http":
        try:
            loopback = parsed.hostname.lower() == "localhost" or ipaddress.ip_address(parsed.hostname).is_loopback
        except ValueError:
            loopback = False
        if not loopback:
            parser.error("use HTTPS unless the endpoint targets loopback")
    if not 0 < args.threshold <= 1:
        parser.error("--threshold must be greater than 0 and at most 1")
    if args.timeout <= 0:
        parser.error("--timeout must be positive")
    api_key = os.environ.get(args.api_key_env, "").strip() if args.api_key_env else ""
    if args.api_key_env and not api_key:
        parser.error("the named API key environment variable is empty")

    rows = load_corpus()
    model_predictions = []
    confidences = []
    models = set()
    routing_models = set()
    for index, row in enumerate(rows, start=1):
        try:
            intent, confidence, model, routing_model = predict(
                args.endpoint, row["text"], args.timeout, api_key, args.model
            )
        except RuntimeError as error:
            print("sample %d: %s" % (index, error), file=sys.stderr)
            return 2
        model_predictions.append(intent)
        confidences.append(confidence)
        if model:
            models.add(model)
        if routing_model:
            routing_models.add(routing_model)

    by_split = {}
    for split in sorted({row["split"] for row in rows}):
        indexes = [i for i, row in enumerate(rows) if row["split"] == split]
        split_rows = [rows[i] for i in indexes]
        laya = [model_predictions[i] for i in indexes]
        conf = [confidences[i] for i in indexes]
        rules = [row["rules_intent"] for row in split_rows]
        gated = [laya[i] if conf[i] >= args.threshold else rules[i] for i in range(len(indexes))]
        covered = [i for i, value in enumerate(conf) if value >= args.threshold]
        by_split[split] = {
            "count": len(split_rows),
            "rules_accuracy": accuracy(split_rows, rules),
            "laya_accuracy": accuracy(split_rows, laya),
            "laya_coverage": len(covered) / len(split_rows),
            "laya_accuracy_when_selected": accuracy([split_rows[i] for i in covered], [laya[i] for i in covered]),
            "gated_accuracy": accuracy(split_rows, gated),
        }

    labels = sorted(CRITERIA)
    matrix = {expected: {predicted: 0 for predicted in labels} for expected in labels}
    for row, predicted in zip(rows, model_predictions):
        matrix[row["expected"]][predicted] += 1
    print(json.dumps({
        "corpus_size": len(rows),
        "threshold": args.threshold,
        "requested_model": args.model or None,
        "models_reported_by_service": sorted(models),
        "routing_models_reported_by_service": sorted(routing_models),
        "metrics_by_split": by_split,
        "laya_confusion_matrix": matrix,
        "confidence_field": "answer_confidence",
        "evidence_note": "Service response measured; verify the reported checkpoint before treating this as model-real evidence.",
    }, indent=2, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
