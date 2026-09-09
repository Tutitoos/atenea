#!/usr/bin/env bash
set -euo pipefail

result="${1:?usage: validate-ios-simulator-visual-result.sh RESULT.json}"
jq -e '
  . as $root |
  .schema_version == "1.0.0" and
  .environment.device == "iPhone 14" and
  .environment.runtime == "iOS 16.4" and
  (.environment.displays | length) >= 2 and
  ([.cycles[].duplicate_actions] | all(. == 0)) and
  ([.cycles[] | select(.classification == "unknown_after_mutation") | .fallbacks] | all(. == 0)) and
  (["official_computer_use", "atenea_desktop_visual", "agent_device"] | all(. as $backend |
    ["R34w-30", "S34CG50"] | all(. as $fixture |
      ["centered", "fitted", "moved", "spanning", "rescaled_capture", "flutter_canvas_no_ax", "rotation", "resize", "human_interrupt"] | all(. as $scenario |
        [$backend, $fixture, $scenario] as $cell |
        [$root.cycles[] | select(.backend_requested == $cell[0] and .fixture == $cell[1] and .scenario == $cell[2])] as $runs |
        ([$runs[] | select(.warmup)] | length == 5) and
        ([$runs[] | select(.warmup | not)] | length == 30) and
        ([$runs[] | select(.warmup | not) | .cycle] | sort == [range(1;31)])
      )
    )
  ))
' "$result" >/dev/null

echo "iOS Simulator visual result has the complete 3 x 2 x 9 x (5 + 30) matrix and no duplicate actions"
