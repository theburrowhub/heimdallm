#!/usr/bin/env python3
"""Fail-closed diff and total coverage ratchet for Heimdallm.

The script intentionally uses only the Python standard library.  Run
``python3 scripts/coverage_gate.py --help`` for the command-line contract and
the baseline schema.
"""

from __future__ import annotations

import argparse
import ast
import json
import re
import subprocess
import sys
from dataclasses import dataclass, field
from fractions import Fraction
from pathlib import Path, PurePosixPath
from typing import Iterable, Mapping, Sequence


MODULE_NAMES = ("daemon", "cli", "flutter")
MINIMUM_POLICY = Fraction(9, 10)
# This is deliberately a code-owned, reviewed allowlist rather than data
# derived from the baseline.  A scope=universe rule passes only when its exact
# path is approved here *and* the baseline supplies a reason and issue.  Adding
# an exemption is therefore a two-part policy change: first review the
# unavoidable collector limitation and add its path here, then add the
# justified baseline entry.  evaluate_gate permits only this kind of new rule;
# coverage exclusions and changes to existing rules remain frozen. Keeping both
# controls independent prevents a baseline-only edit from exempting arbitrary
# product source.
UNIVERSE_EXEMPT_PATHS = frozenset(
    {
        "flutter_app/lib/core/platform/platform_services_stub.dart",
        "flutter_app/lib/core/platform/platform_services_web.dart",
    }
)
IGNORE_DIRECTIVE_RE = re.compile(r"\bcoverage:ignore\b")
GENERATED_SOURCE_RE = re.compile(
    r"(?m)^// (?:Code generated .* DO NOT EDIT\.|GENERATED CODE - DO NOT MODIFY BY HAND)$"
)
GO_BLOCK_RE = re.compile(
    r"^(.+):(\d+)\.(\d+),(\d+)\.(\d+)\s+(\d+)\s+(\d+)$"
)
HUNK_RE = re.compile(
    r"^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(?: .*)?$"
)
ISSUE_RE = re.compile(r"(?:#\d+|https://github\.com/[^/]+/[^/]+/(?:issues|pull)/\d+)$")


class GateError(Exception):
    """A policy, input, report, or repository error (exit status 1)."""


@dataclass(frozen=True)
class ModuleSpec:
    name: str
    root: str
    source_prefix: str
    suffix: str
    report_format: str

    def is_product_source(self, path: str) -> bool:
        if not path.startswith(self.source_prefix) or not path.endswith(self.suffix):
            return False
        return not (self.suffix == ".go" and path.endswith("_test.go"))


MODULES: Mapping[str, ModuleSpec] = {
    "daemon": ModuleSpec("daemon", "daemon", "daemon/", ".go", "go"),
    "cli": ModuleSpec("cli", "cli", "cli/", ".go", "go"),
    "flutter": ModuleSpec(
        "flutter", "flutter_app", "flutter_app/lib/", ".dart", "lcov"
    ),
}


@dataclass(frozen=True)
class CoverageUnit:
    path: str
    start_line: int
    end_line: int
    weight: int
    covered: bool


@dataclass
class CoverageReport:
    files: dict[str, list[CoverageUnit]] = field(default_factory=dict)

    def add_file(self, path: str) -> None:
        self.files.setdefault(path, [])


@dataclass(frozen=True)
class IgnoreRule:
    module: str
    path: str
    reason: str
    issue: str
    scope: str = "coverage"
    line_ranges: tuple[tuple[int, int], ...] | None = None

    @property
    def whole_file(self) -> bool:
        return self.line_ranges is None

    def ignores_line(self, line: int) -> bool:
        return self.whole_file or any(start <= line <= end for start, end in self.line_ranges or ())

    def ignores_unit(self, unit: CoverageUnit) -> bool:
        # Go's unit is a basic block.  A line-specific exemption is anchored at
        # its first executable line so it cannot accidentally remove adjacent
        # blocks merely because their lexical range overlaps.
        return self.ignores_line(unit.start_line)

    @property
    def excludes_coverage(self) -> bool:
        return self.scope == "coverage"


@dataclass(frozen=True)
class ModuleBaseline:
    covered: int
    total: int

    @property
    def ratio(self) -> Fraction:
        return Fraction(self.covered, self.total)


@dataclass(frozen=True)
class Baseline:
    target: Fraction
    diff_target: Fraction
    modules: Mapping[str, ModuleBaseline]
    ignores: tuple[IgnoreRule, ...]


@dataclass(frozen=True)
class ModuleResult:
    name: str
    covered: int
    total: int
    diff_covered: int
    diff_total: int
    hard_floor: bool


def _fraction_percent(value: object, field_name: str) -> Fraction:
    if isinstance(value, bool) or not isinstance(value, (int, float, str)):
        raise GateError(f"{field_name} must be a number between 0 and 100")
    try:
        result = Fraction(str(value)) / 100
    except (ValueError, ZeroDivisionError) as exc:
        raise GateError(f"{field_name} must be a number between 0 and 100") from exc
    if result < 0 or result > 1:
        raise GateError(f"{field_name} must be a number between 0 and 100")
    return result


def _parse_line_ranges(value: object, where: str) -> tuple[tuple[int, int], ...]:
    if not isinstance(value, list) or not value:
        raise GateError(f"{where}.lines must be a non-empty array")
    ranges: list[tuple[int, int]] = []
    for item in value:
        if isinstance(item, bool):
            raise GateError(f"{where}.lines entries must be positive lines or [start, end]")
        if isinstance(item, int):
            start = end = item
        elif (
            isinstance(item, list)
            and len(item) == 2
            and all(isinstance(part, int) and not isinstance(part, bool) for part in item)
        ):
            start, end = item
        else:
            raise GateError(f"{where}.lines entries must be positive lines or [start, end]")
        if start < 1 or end < start:
            raise GateError(f"{where}.lines contains an invalid range {item!r}")
        ranges.append((start, end))
    ranges.sort()
    for previous, current in zip(ranges, ranges[1:]):
        if current[0] <= previous[1]:
            raise GateError(f"{where}.lines contains overlapping ranges")
    return tuple(ranges)


def _safe_repo_path(raw: str, where: str) -> str:
    if not raw or "\x00" in raw or "\\" in raw:
        raise GateError(f"{where} is not a valid repository-relative POSIX path")
    path = PurePosixPath(raw)
    if path.is_absolute() or any(part in ("", ".", "..") for part in path.parts):
        raise GateError(f"{where} is not a valid repository-relative POSIX path")
    return path.as_posix()


def parse_baseline_text(text: str, source: str = "baseline") -> Baseline:
    if not text.strip():
        raise GateError(f"{source} is empty")
    try:
        data = json.loads(text)
    except json.JSONDecodeError as exc:
        raise GateError(f"{source} is malformed JSON: line {exc.lineno}: {exc.msg}") from exc
    if not isinstance(data, dict):
        raise GateError(f"{source} must contain a JSON object")
    if data.get("version") != 1:
        raise GateError(f"{source}.version must be 1")
    target = _fraction_percent(data.get("target_percent"), f"{source}.target_percent")
    diff_target = _fraction_percent(
        data.get("diff_percent"), f"{source}.diff_percent"
    )
    if target < MINIMUM_POLICY:
        raise GateError(f"{source}.target_percent must be at least 90")
    if diff_target < MINIMUM_POLICY:
        raise GateError(f"{source}.diff_percent must be at least 90")
    modules_data = data.get("modules")
    if not isinstance(modules_data, dict) or set(modules_data) != set(MODULE_NAMES):
        raise GateError(f"{source}.modules must contain exactly daemon, cli, and flutter")
    modules: dict[str, ModuleBaseline] = {}
    for name in MODULE_NAMES:
        item = modules_data[name]
        if not isinstance(item, dict):
            raise GateError(f"{source}.modules.{name} must be an object")
        covered, total = item.get("covered"), item.get("total")
        if (
            isinstance(covered, bool)
            or isinstance(total, bool)
            or not isinstance(covered, int)
            or not isinstance(total, int)
            or covered < 0
            or total <= 0
            or covered > total
        ):
            raise GateError(
                f"{source}.modules.{name} needs integer counts 0 <= covered <= total"
            )
        modules[name] = ModuleBaseline(covered, total)

    ignores_data = data.get("ignore", [])
    if not isinstance(ignores_data, list):
        raise GateError(f"{source}.ignore must be an array")
    ignores: list[IgnoreRule] = []
    seen: set[tuple[str, str]] = set()
    for index, item in enumerate(ignores_data):
        where = f"{source}.ignore[{index}]"
        if not isinstance(item, dict):
            raise GateError(f"{where} must be an object")
        allowed = {"module", "path", "reason", "issue", "scope", "lines"}
        unknown = set(item) - allowed
        if unknown:
            raise GateError(f"{where} has unknown fields: {', '.join(sorted(unknown))}")
        module, raw_path = item.get("module"), item.get("path")
        reason, issue = item.get("reason"), item.get("issue")
        scope = item.get("scope", "coverage")
        if module not in MODULES:
            raise GateError(f"{where}.module must be daemon, cli, or flutter")
        if not isinstance(raw_path, str):
            raise GateError(f"{where}.path must be a string")
        path = _safe_repo_path(raw_path, f"{where}.path")
        if any(char in path for char in "*?["):
            raise GateError(f"{where}.path must be exact; glob patterns are forbidden")
        if not MODULES[module].is_product_source(path):
            raise GateError(f"{where}.path is not a production source in {module}")
        if not isinstance(reason, str) or not reason.strip():
            raise GateError(f"{where}.reason must be a non-empty justification")
        if not isinstance(issue, str) or not ISSUE_RE.fullmatch(issue):
            raise GateError(f"{where}.issue must be #<number> or a GitHub issue/PR URL")
        if scope not in ("coverage", "universe"):
            raise GateError(f"{where}.scope must be coverage or universe")
        if scope == "universe" and "lines" in item:
            raise GateError(f"{where}.lines is incompatible with scope universe")
        if scope == "universe" and path not in UNIVERSE_EXEMPT_PATHS:
            raise GateError(
                f"{where}.path is not an approved collector universe exemption"
            )
        key = (module, path)
        if key in seen:
            raise GateError(f"{source}.ignore repeats {path}")
        seen.add(key)
        line_ranges = (
            _parse_line_ranges(item["lines"], where) if "lines" in item else None
        )
        ignores.append(IgnoreRule(module, path, reason.strip(), issue, scope, line_ranges))
    return Baseline(target, diff_target, modules, tuple(ignores))


def _normalise_report_path(raw: str, spec: ModuleSpec) -> str:
    raw = raw.strip().replace("\\", "/")
    if not raw or "\x00" in raw:
        raise GateError(f"{spec.name} report contains an empty or invalid source path")
    parts = [part for part in PurePosixPath(raw).parts if part not in ("/", ".")]
    if ".." in parts:
        raise GateError(f"{spec.name} report path escapes the module: {raw}")
    if spec.root in parts:
        # Import paths (github.com/.../daemon/...) and absolute checkout paths
        # both become repository-relative at the module-root segment.
        parts = parts[parts.index(spec.root) :]
    elif not PurePosixPath(raw).is_absolute():
        parts = [spec.root, *parts]
    elif spec.name == "flutter" and "lib" in parts:
        parts = [spec.root, *parts[parts.index("lib") :]]
    else:
        raise GateError(f"{spec.name} report path is outside {spec.root}: {raw}")
    return _safe_repo_path("/".join(parts), f"{spec.name} report path")


def parse_go_profile_text(
    text: str, spec: ModuleSpec, source: str = "Go coverage profile"
) -> CoverageReport:
    if not text.strip():
        raise GateError(f"{source} is empty")
    blocks: dict[tuple[str, int, int, int, int], tuple[int, bool]] = {}
    saw_header = False
    for line_number, raw_line in enumerate(text.splitlines(), 1):
        line = raw_line.strip()
        if not line:
            # Empty separators carry no coverage data. Tolerating them keeps
            # harmless producer formatting changes out of the policy verdict.
            continue
        if line.startswith("mode:"):
            if line != "mode: atomic":
                raise GateError(f"{source}:{line_number}: mode must be atomic")
            saw_header = True
            continue
        if not saw_header:
            raise GateError(f"{source}:{line_number}: missing 'mode: atomic' header")
        match = GO_BLOCK_RE.fullmatch(line)
        if not match:
            raise GateError(f"{source}:{line_number}: malformed Go coverage block")
        raw_path, sl, sc, el, ec, statements, count = match.groups()
        path = _normalise_report_path(raw_path, spec)
        sl_i, sc_i, el_i, ec_i = map(int, (sl, sc, el, ec))
        statements_i, count_i = int(statements), int(count)
        if (
            sl_i < 1
            or sc_i < 1
            or el_i < 1
            or ec_i < 1
            or (el_i, ec_i) < (sl_i, sc_i)
            # cmd/cover emits valid zero-width, zero-statement blocks for some
            # control-flow edges.  Preserve them for file presence/dedup but
            # their zero weight naturally contributes nothing to coverage.
            or statements_i < 0
            or count_i < 0
        ):
            raise GateError(f"{source}:{line_number}: invalid Go coverage values")
        key = (path, sl_i, sc_i, el_i, ec_i)
        previous = blocks.get(key)
        if previous and previous[0] != statements_i:
            raise GateError(
                f"{source}:{line_number}: duplicate block has conflicting statement count"
            )
        blocks[key] = (statements_i, count_i > 0 or bool(previous and previous[1]))
    if not saw_header:
        raise GateError(f"{source} has no mode header")
    if not blocks:
        raise GateError(f"{source} has no coverage blocks")
    report = CoverageReport()
    for (path, sl, _sc, el, _ec), (weight, covered) in sorted(blocks.items()):
        if spec.is_product_source(path):
            report.add_file(path)
            report.files[path].append(CoverageUnit(path, sl, el, weight, covered))
    if not report.files:
        raise GateError(f"{source} contains no production coverage blocks")
    return report


_LCOV_ALLOWED_PREFIXES = {
    "TN",
    "VER",
    "SF",
    "FN",
    "FNDA",
    "FNF",
    "FNH",
    "DA",
    "BRDA",
    "BRF",
    "BRH",
    "BA",
    "LF",
    "LH",
}


def parse_lcov_text(
    text: str, spec: ModuleSpec = MODULES["flutter"], source: str = "LCOV report"
) -> CoverageReport:
    if not text.strip():
        raise GateError(f"{source} is empty")
    merged: dict[tuple[str, int], bool] = {}
    present_files: set[str] = set()
    current_path: str | None = None
    record_lines: dict[int, bool] = {}
    saw_record = False

    def finish_record(line_number: int) -> None:
        nonlocal current_path, record_lines, saw_record
        if current_path is None:
            raise GateError(f"{source}:{line_number}: end_of_record without SF")
        hit = sum(record_lines.values())
        if spec.is_product_source(current_path):
            present_files.add(current_path)
            for line, covered in record_lines.items():
                key = (current_path, line)
                merged[key] = merged.get(key, False) or covered
        saw_record = True
        current_path = None
        record_lines = {}

    for line_number, raw_line in enumerate(text.splitlines(), 1):
        line = raw_line.strip()
        if not line:
            # LCOV tracefiles may use whitespace-only lines as separators.  A
            # separator carries no record data, so ignoring it is lossless.
            continue
        if line == "end_of_record":
            finish_record(line_number)
            continue
        if ":" not in line:
            raise GateError(f"{source}:{line_number}: malformed LCOV record")
        tag, value = line.split(":", 1)
        if tag not in _LCOV_ALLOWED_PREFIXES:
            raise GateError(f"{source}:{line_number}: unknown LCOV tag {tag!r}")
        if tag == "TN" or tag == "VER":
            if current_path is not None:
                raise GateError(f"{source}:{line_number}: {tag} appears inside a record")
        elif tag == "SF":
            if current_path is not None:
                raise GateError(f"{source}:{line_number}: nested SF record")
            current_path = _normalise_report_path(value, spec)
        elif current_path is None:
            raise GateError(f"{source}:{line_number}: {tag} appears before SF")
        elif tag == "DA":
            parts = value.split(",")
            if len(parts) not in (2, 3):
                raise GateError(f"{source}:{line_number}: malformed DA record")
            try:
                source_line, count = int(parts[0]), int(parts[1])
            except ValueError as exc:
                raise GateError(f"{source}:{line_number}: malformed DA record") from exc
            if source_line < 1 or count < 0:
                raise GateError(f"{source}:{line_number}: invalid DA values")
            record_lines[source_line] = record_lines.get(source_line, False) or count > 0
        elif tag in ("LF", "LH"):
            # DA records are authoritative for the gate. LF/LH are redundant
            # producer summaries, so validate their syntax but keep them
            # advisory to tolerate valid formatter differences.
            try:
                count = int(value)
            except ValueError as exc:
                raise GateError(f"{source}:{line_number}: malformed {tag} record") from exc
            if count < 0:
                raise GateError(f"{source}:{line_number}: invalid {tag} value")
        # Function and branch records are intentionally syntax-tolerant: line
        # coverage is authoritative, but unknown tags still fail closed.
    if current_path is not None:
        raise GateError(f"{source}: final record is missing end_of_record")
    if not saw_record:
        raise GateError(f"{source} has no LCOV records")
    if not merged:
        raise GateError(f"{source} has no production DA records")
    report = CoverageReport()
    for path in sorted(present_files):
        report.add_file(path)
    for (path, line), covered in sorted(merged.items()):
        report.files[path].append(CoverageUnit(path, line, line, 1, covered))
    return report


def _decode_patch_path(raw: str) -> str:
    value = raw.strip()
    if value == "/dev/null":
        return value
    if value.startswith('"'):
        try:
            decoded = ast.literal_eval(value)
        except (SyntaxError, ValueError) as exc:
            raise GateError(f"malformed quoted path in git diff: {raw}") from exc
        if not isinstance(decoded, str):
            raise GateError(f"malformed quoted path in git diff: {raw}")
        value = decoded
    if value.startswith(("a/", "b/")):
        value = value[2:]
    return _safe_repo_path(value, "git diff path")


def parse_unified_diff(text: str, source: str = "git diff") -> dict[str, set[int]]:
    changed: dict[str, set[int]] = {}
    current_path: str | None = None
    hunk: dict[str, int] | None = None

    def finish_hunk(line_number: int) -> None:
        nonlocal hunk
        if hunk is not None and (
            hunk["old_seen"] != hunk["old_count"]
            or hunk["new_seen"] != hunk["new_count"]
        ):
            raise GateError(f"{source}:{line_number}: hunk line counts do not match header")
        hunk = None

    lines = text.splitlines()
    for line_number, line in enumerate(lines, 1):
        if line.startswith("diff --git "):
            finish_hunk(line_number)
            current_path = None
            continue
        if line.startswith("+++ "):
            finish_hunk(line_number)
            path = _decode_patch_path(line[4:])
            current_path = None if path == "/dev/null" else path
            if current_path is not None:
                changed.setdefault(current_path, set())
            continue
        match = HUNK_RE.fullmatch(line)
        if match:
            finish_hunk(line_number)
            if current_path is None:
                # A deletion hunk has +0,0 and no current path; it contributes
                # no lines.  Any additions without a destination are malformed.
                if int(match.group(4) or 1) != 0:
                    raise GateError(f"{source}:{line_number}: hunk has no destination file")
            old_count = int(match.group(2) or 1)
            new_count = int(match.group(4) or 1)
            hunk = {
                "new_line": int(match.group(3)),
                "old_count": old_count,
                "new_count": new_count,
                "old_seen": 0,
                "new_seen": 0,
            }
            continue
        if hunk is None:
            continue
        if line.startswith("\\ No newline at end of file"):
            continue
        if not line:
            raise GateError(f"{source}:{line_number}: malformed hunk line")
        marker = line[0]
        if marker == "+":
            if current_path is None:
                raise GateError(f"{source}:{line_number}: addition has no destination file")
            changed[current_path].add(hunk["new_line"])
            hunk["new_line"] += 1
            hunk["new_seen"] += 1
        elif marker == "-":
            hunk["old_seen"] += 1
        elif marker == " ":
            hunk["old_seen"] += 1
            hunk["new_seen"] += 1
            hunk["new_line"] += 1
        else:
            raise GateError(f"{source}:{line_number}: malformed hunk line")
    finish_hunk(len(lines) + 1)
    return changed


def _run_git(repo_root: Path, arguments: Sequence[str], description: str) -> bytes:
    try:
        process = subprocess.run(
            ["git", *arguments],
            cwd=repo_root,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
        )
    except OSError as exc:
        raise GateError(f"cannot run git for {description}: {exc}") from exc
    if process.returncode:
        detail = process.stderr.decode("utf-8", "replace").strip()
        raise GateError(f"git failed while {description}: {detail or 'unknown error'}")
    return process.stdout


def _resolve_base(repo_root: Path, base_ref: str) -> str:
    if not base_ref or "\x00" in base_ref:
        raise GateError("--base-ref must not be empty")
    output = _run_git(
        repo_root,
        ["rev-parse", "--verify", "--end-of-options", f"{base_ref}^{{commit}}"],
        "resolving --base-ref",
    )
    sha = output.decode("ascii", "strict").strip()
    if not re.fullmatch(r"[0-9a-fA-F]{40,64}", sha):
        raise GateError("git returned an invalid base commit")
    return sha


def _path_in_repo(repo_root: Path, path: Path, option: str) -> tuple[Path, str]:
    absolute = path if path.is_absolute() else repo_root / path
    absolute = absolute.resolve()
    root = repo_root.resolve()
    try:
        relative = absolute.relative_to(root)
    except ValueError as exc:
        raise GateError(f"{option} must be inside the repository") from exc
    return absolute, _safe_repo_path(relative.as_posix(), option)


def _read_required(path: Path, description: str) -> str:
    try:
        data = path.read_text(encoding="utf-8")
    except (OSError, UnicodeError) as exc:
        raise GateError(f"cannot read {description} at {path}: {exc}") from exc
    if not data.strip():
        raise GateError(f"{description} at {path} is empty")
    return data


def _base_baseline(
    repo_root: Path, base_sha: str, relative_path: str
) -> Baseline | None:
    object_name = f"{base_sha}:{relative_path}"
    listed = _run_git(
        repo_root,
        ["ls-tree", "-z", "--name-only", base_sha, "--", relative_path],
        "checking for the baseline in the base commit",
    )
    try:
        listed_paths = [item for item in listed.decode("utf-8").split("\x00") if item]
    except UnicodeError as exc:
        raise GateError("git returned a non-UTF-8 baseline path") from exc
    if not listed_paths:
        return None
    if listed_paths != [relative_path]:
        raise GateError("git returned an unexpected baseline path from the base commit")
    raw = _run_git(repo_root, ["show", object_name], "reading baseline from base commit")
    try:
        text = raw.decode("utf-8")
    except UnicodeError as exc:
        raise GateError("baseline in base commit is not UTF-8") from exc
    return parse_baseline_text(text, f"{relative_path} at {base_sha[:12]}")


def _git_paths(repo_root: Path, *arguments: str) -> list[str]:
    raw = _run_git(repo_root, ["ls-files", "-z", *arguments], "listing source files")
    try:
        decoded = raw.decode("utf-8")
    except UnicodeError as exc:
        raise GateError("git returned a non-UTF-8 source path") from exc
    return [_safe_repo_path(item, "git source path") for item in decoded.split("\x00") if item]


def _source_inventory(repo_root: Path) -> set[str]:
    paths = _git_paths(repo_root, "--cached", "--others", "--exclude-standard", "--")
    return {
        path
        for path in paths
        if (repo_root / path).is_file()
        and any(spec.is_product_source(path) for spec in MODULES.values())
    }


def _repository_diff(repo_root: Path, base_sha: str, inventory: set[str]) -> dict[str, set[int]]:
    raw = _run_git(
        repo_root,
        [
            "-c",
            "core.quotePath=false",
            "diff",
            "--unified=0",
            "--no-ext-diff",
            "--no-color",
            "--find-renames",
            base_sha,
            "--",
        ],
        "reading the base-to-working-tree diff",
    )
    try:
        changed = parse_unified_diff(raw.decode("utf-8"))
    except UnicodeError as exc:
        raise GateError("git diff is not UTF-8") from exc
    untracked = set(_git_paths(repo_root, "--others", "--exclude-standard", "--"))
    for path in untracked & inventory:
        try:
            line_count = len((repo_root / path).read_text(encoding="utf-8").splitlines())
        except (OSError, UnicodeError) as exc:
            raise GateError(f"cannot read untracked source {path}: {exc}") from exc
        changed[path] = set(range(1, line_count + 1))
    return changed


def _rules_by_path(baseline: Baseline, module: str) -> dict[str, IgnoreRule]:
    return {rule.path: rule for rule in baseline.ignores if rule.module == module}


def _coverage_rules_by_path(baseline: Baseline, module: str) -> dict[str, IgnoreRule]:
    return {
        rule.path: rule
        for rule in baseline.ignores
        if rule.module == module and rule.excludes_coverage
    }


def _validate_ignores_and_directives(
    repo_root: Path, baseline: Baseline, inventory: set[str]
) -> None:
    rules = {(rule.module, rule.path): rule for rule in baseline.ignores}
    for rule in baseline.ignores:
        if rule.path not in inventory:
            raise GateError(f"configured ignore does not name a current production file: {rule.path}")
        try:
            source_text = (repo_root / rule.path).read_text(encoding="utf-8")
        except (OSError, UnicodeError) as exc:
            raise GateError(f"cannot validate configured ignore {rule.path}: {exc}") from exc
        if rule.scope == "coverage" and rule.whole_file and not GENERATED_SOURCE_RE.search(source_text):
            raise GateError(
                f"full coverage ignore is allowed only for generated source with a standard marker: {rule.path}"
            )
    for path in sorted(inventory):
        try:
            text = (repo_root / path).read_text(encoding="utf-8")
        except (OSError, UnicodeError) as exc:
            raise GateError(f"cannot inspect coverage directives in {path}: {exc}") from exc
        if not IGNORE_DIRECTIVE_RE.search(text):
            continue
        module = next(name for name, spec in MODULES.items() if spec.is_product_source(path))
        rule = rules.get((module, path))
        if rule is None or not rule.excludes_coverage:
            raise GateError(
                f"{path} contains coverage:ignore but has no justified scope=coverage baseline ignore entry"
            )


def _filtered_counts(report: CoverageReport, rules: Mapping[str, IgnoreRule]) -> tuple[int, int]:
    covered = total = 0
    for path, units in report.files.items():
        rule = rules.get(path)
        for unit in units:
            if rule and rule.ignores_unit(unit):
                continue
            total += unit.weight
            if unit.covered:
                covered += unit.weight
    if total <= 0:
        raise GateError("coverage report has no non-ignored executable units")
    return covered, total


def _diff_counts(
    report: CoverageReport,
    changed: Mapping[str, set[int]],
    rules: Mapping[str, IgnoreRule],
) -> tuple[int, int]:
    covered = total = 0
    for path, units in report.files.items():
        lines = changed.get(path, set())
        rule = rules.get(path)
        if rule:
            lines = {line for line in lines if not rule.ignores_line(line)}
        if not lines:
            continue
        for unit in units:
            if rule and rule.ignores_unit(unit):
                continue
            if any(unit.start_line <= line <= unit.end_line for line in lines):
                total += unit.weight
                if unit.covered:
                    covered += unit.weight
    return covered, total


def evaluate_gate(
    baseline: Baseline,
    base_baseline: Baseline | None,
    reports: Mapping[str, CoverageReport],
    inventory: set[str],
    changed: Mapping[str, set[int]],
) -> tuple[list[ModuleResult], list[str]]:
    failures: list[str] = []
    if base_baseline is not None:
        if baseline.target < base_baseline.target:
            failures.append("target_percent is lower than the base baseline")
        if baseline.diff_target < base_baseline.diff_target:
            failures.append("diff_percent is lower than the base baseline")
        base_ignores = {
            (rule.module, rule.path): rule for rule in base_baseline.ignores
        }
        unsafe_ignores = []
        for rule in baseline.ignores:
            previous = base_ignores.get((rule.module, rule.path))
            if (
                previous is None
                and rule.scope == "universe"
                and rule.path in inventory
            ):
                # parse_baseline_text has already required the independent,
                # code-reviewed UNIVERSE_EXEMPT_PATHS approval. Requiring the
                # source to exist prevents pre-authorising a future file.
                continue
            if previous != rule:
                unsafe_ignores.append(rule)
        if unsafe_ignores:
            paths = ", ".join(sorted(rule.path for rule in unsafe_ignores))
            failures.append(
                "coverage ignore policy was added, or an existing ignore was "
                "changed or widened relative to the base baseline: " + paths
            )
    results: list[ModuleResult] = []
    for name in MODULE_NAMES:
        spec = MODULES[name]
        report = reports[name]
        all_rules = _rules_by_path(baseline, name)
        rules = _coverage_rules_by_path(baseline, name)
        module_inventory = {path for path in inventory if spec.is_product_source(path)}
        report_paths = set(report.files)
        extra = report_paths - module_inventory
        if extra:
            failures.append(f"{name}: report contains stale/untracked sources: {', '.join(sorted(extra))}")

        whole_file_ignores = {path for path, rule in rules.items() if rule.whole_file}
        universe_exempt = {
            path for path, rule in all_rules.items() if rule.scope == "universe"
        }
        if name == "flutter":
            required = module_inventory - whole_file_ignores - universe_exempt
            # Universe-only exemptions solve collector limitations, not diff
            # coverage: changing one makes report presence mandatory again.
            required.update(
                path
                for path in changed
                if spec.is_product_source(path)
                and path in module_inventory
                and path not in whole_file_ignores
            )
        else:
            required = {
                path
                for path in changed
                if spec.is_product_source(path)
                and path in module_inventory
                and path not in whole_file_ignores
            }
        missing = required - report_paths
        if missing:
            failures.append(
                f"{name}: production sources are absent from the coverage report: "
                + ", ".join(sorted(missing))
            )

        covered, total = _filtered_counts(report, rules)
        declared = baseline.modules[name]
        if (covered, total) != (declared.covered, declared.total):
            failures.append(
                f"{name}: report is {covered}/{total}, baseline declares "
                f"{declared.covered}/{declared.total}; update the exact ratchet"
            )
        if base_baseline is not None:
            old = base_baseline.modules[name]
            if declared.ratio < old.ratio:
                failures.append(
                    f"{name}: baseline ratio fell from {old.covered}/{old.total} "
                    f"to {declared.covered}/{declared.total}"
                )
        diff_covered, diff_total = _diff_counts(report, changed, rules)
        if diff_total and Fraction(diff_covered, diff_total) < baseline.diff_target:
            failures.append(
                f"{name}: diff coverage {diff_covered}/{diff_total} is below "
                f"{_format_percent(baseline.diff_target)}"
            )
        hard_floor = declared.ratio >= baseline.target
        if hard_floor and Fraction(covered, total) < baseline.target:
            failures.append(
                f"{name}: total coverage is below hard floor {_format_percent(baseline.target)}"
            )
        results.append(
            ModuleResult(name, covered, total, diff_covered, diff_total, hard_floor)
        )
    return results, failures


def _format_percent(ratio: Fraction) -> str:
    return f"{float(ratio * 100):.2f}%"


def render_summary(
    results: Iterable[ModuleResult],
    baseline: Baseline,
    failures: Sequence[str],
    bootstrap: bool,
) -> str:
    rows = [
        "## Coverage gate",
        "",
        "| Module | Diff coverage | Total coverage | Baseline | Total gate | Result |",
        "|---|---:|---:|---:|---|---|",
    ]
    failed_modules = {
        failure.split(":", 1)[0] for failure in failures if ":" in failure
    }
    for result in results:
        diff = (
            f"{result.diff_covered}/{result.diff_total} "
            f"({_format_percent(Fraction(result.diff_covered, result.diff_total))})"
            if result.diff_total
            else "n/a (no executable changed lines)"
        )
        total = f"{result.covered}/{result.total} ({_format_percent(Fraction(result.covered, result.total))})"
        declared = baseline.modules[result.name]
        floor = f"hard floor {_format_percent(baseline.target)}" if result.hard_floor else "exact ratchet"
        status = "FAIL" if result.name in failed_modules else "PASS"
        rows.append(
            f"| {result.name} | {diff} | {total} | {declared.covered}/{declared.total} | {floor} | {status} |"
        )
    rows.extend(["", f"**Result: {'FAIL' if failures else 'PASS'}**"])
    if bootstrap:
        rows.extend(["", "Bootstrap: the valid base commit has no baseline file yet."])
    if failures:
        rows.extend(["", "Failures:", ""])
        rows.extend(f"- {failure}" for failure in failures)
    return "\n".join(rows) + "\n"


def _append_summary(path: Path, summary: str) -> None:
    try:
        with path.open("a", encoding="utf-8") as handle:
            handle.write(summary)
    except OSError as exc:
        raise GateError(f"cannot append --summary {path}: {exc}") from exc


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Enforce Heimdallm's exact total-coverage ratchet and diff floor.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""baseline JSON schema (paths are repository-relative and exact):
  {
    "version": 1,
    "target_percent": 90,
    "diff_percent": 90,
    "modules": {
      "daemon": {"covered": 8437, "total": 12340},
      "cli": {"covered": 801, "total": 2549},
      "flutter": {"covered": 4463, "total": 8427}
    },
    "ignore": [
      {"module": "flutter", "path": "flutter_app/lib/generated.g.dart",
       "reason": "generated code", "issue": "#688"},
      {"module": "flutter", "path": "flutter_app/lib/web_adapter.dart",
       "reason": "collector limitation", "issue": "#688", "scope": "universe"},
      {"module": "flutter", "path": "flutter_app/lib/main.dart",
       "reason": "instrumentation inconsistency", "issue": "#688", "lines": [375]}
    ]
  }

The current report counts must equal the current baseline exactly.  Its ratio
may not be lower than the baseline stored at BASE_REF.  A missing base copy is
allowed only for the initial bootstrap.  Go profiles must use mode: atomic.
scope=coverage (the default) excludes total, universe, and diff coverage.
scope=universe only permits a collector to omit an unchanged file; changing it
still requires report presence and coverage.  Universe exemptions are
double-controlled: the exact path must also be in the code-reviewed
UNIVERSE_EXEMPT_PATHS allowlist. Adding one requires two explicit edits (the
allowlist after reviewing the collector limitation, and a justified baseline
entry). Only new universe rules use this path; coverage exclusions and edits to
existing rules remain frozen. The allowlist is deliberately not derived from
the baseline. `lines` narrows scope=coverage to exact lines (or inclusive
`[start, end]` pairs). Deleted lines do not enter diff coverage. --summary
appends Markdown suitable
for GITHUB_STEP_SUMMARY. Exit 0 passes; exit 1 is fail-closed; argparse uses 2.
""",
    )
    parser.add_argument("--base-ref", required=True, help="Git ref/commit to compare against")
    parser.add_argument("--daemon-profile", required=True, type=Path, help="daemon Go atomic coverprofile")
    parser.add_argument("--cli-profile", required=True, type=Path, help="CLI Go atomic coverprofile")
    parser.add_argument("--flutter-profile", required=True, type=Path, help="Flutter LCOV report")
    parser.add_argument(
        "--baseline",
        type=Path,
        default=Path(".github/coverage-baseline.json"),
        help="versioned baseline JSON (default: .github/coverage-baseline.json)",
    )
    parser.add_argument("--summary", type=Path, help="append Markdown result to this file")
    return parser


def run(args: argparse.Namespace, repo_root: Path) -> tuple[str, bool]:
    repo_root = repo_root.resolve()
    base_sha = _resolve_base(repo_root, args.base_ref)
    baseline_path, baseline_relative = _path_in_repo(
        repo_root, args.baseline, "--baseline"
    )
    baseline = parse_baseline_text(
        _read_required(baseline_path, "baseline"), baseline_relative
    )
    base = _base_baseline(repo_root, base_sha, baseline_relative)
    inventory = _source_inventory(repo_root)
    _validate_ignores_and_directives(repo_root, baseline, inventory)

    report_paths = {
        "daemon": args.daemon_profile,
        "cli": args.cli_profile,
        "flutter": args.flutter_profile,
    }
    reports: dict[str, CoverageReport] = {}
    for name, supplied_path in report_paths.items():
        path, _ = _path_in_repo(repo_root, supplied_path, f"--{name}-profile")
        text = _read_required(path, f"{name} coverage report")
        spec = MODULES[name]
        reports[name] = (
            parse_go_profile_text(text, spec, str(path))
            if spec.report_format == "go"
            else parse_lcov_text(text, spec, str(path))
        )
    changed = _repository_diff(repo_root, base_sha, inventory)
    results, failures = evaluate_gate(baseline, base, reports, inventory, changed)
    summary = render_summary(results, baseline, failures, base is None)
    if args.summary:
        summary_path = args.summary if args.summary.is_absolute() else repo_root / args.summary
        _append_summary(summary_path, summary)
    return summary, not failures


def main(argv: Sequence[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    repo_root = Path(__file__).resolve().parent.parent
    try:
        summary, passed = run(args, repo_root)
    except GateError as exc:
        message = f"## Coverage gate\n\n**Result: FAIL**\n\n- {exc}\n"
        print(f"coverage gate: FAIL: {exc}", file=sys.stderr)
        if args.summary:
            summary_path = args.summary if args.summary.is_absolute() else repo_root / args.summary
            try:
                _append_summary(summary_path, message)
            except GateError as summary_error:
                print(f"coverage gate: FAIL: {summary_error}", file=sys.stderr)
        return 1
    print(summary, end="")
    return 0 if passed else 1


if __name__ == "__main__":
    raise SystemExit(main())
