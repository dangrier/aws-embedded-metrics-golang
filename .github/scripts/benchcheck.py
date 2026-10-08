#!/usr/bin/env python3
"""Reads benchstat CSV on stdin and fails if head is slower than base.

benchstat only shows a change when it is statistically significant
(otherwise "~"), so noise does not fail the check. Allocations are exact,
so any increase fails. Time and memory fail past a threshold, since shared
CI machines are noisy.
"""
import csv
import sys

# Largest allowed increase, in percent, for each unit.
THRESHOLDS = {"sec/op": 20.0, "B/op": 10.0, "allocs/op": 0.0}

unit = None
regressions = []
for row in csv.reader(sys.stdin):
    if len(row) < 6:
        continue
    if row[0] == "" and row[1] in THRESHOLDS:
        unit = row[1]
        continue
    name, delta = row[0], row[5]
    if unit is None or name in ("", "geomean") or not delta.endswith("%"):
        continue
    change = float(delta.rstrip("%"))
    if change > THRESHOLDS[unit]:
        regressions.append(f"{name}: {unit} {delta} (limit +{THRESHOLDS[unit]:g}%)")

if regressions:
    print("Performance regressions:")
    for r in regressions:
        print(f"  {r}")
    sys.exit(1)
print("No performance regressions.")
