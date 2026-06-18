# VHS tape for the funcd CLI demo. Render with:  vhs docs/demo/cli-demo.tape
# (needs: vhs + ffmpeg + ttyd; run from the repo root, node on PATH)
Output docs/demo/cli-demo.gif
Output docs/demo/cli-demo.webm

Require bash
Require go
Require node
Require yq

Set Shell bash
Set FontSize 16
Set Width 1200
Set Height 560
Set Padding 22
Set Theme "Catppuccin Mocha"

# --- setup: build + boot the platform, HIDDEN (not part of the recording) ---
Hide
Type "bash scripts/demo/setup.sh && clear" Enter
Sleep 9s
Show

# --- the journey: narrated, fully visible from the first line to the last ---
Type "bash scripts/demo/journey.sh" Enter
Sleep 13s

# --- teardown, hidden ---
Hide
Type "bash scripts/demo/teardown.sh" Enter
Sleep 500ms
