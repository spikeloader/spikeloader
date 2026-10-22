---
mode: agent
description: Verify a change end to end (tests + real pipeline run)
---
Verify the current changes:

1. `make test` — must pass.
2. Start the daemon on a spare port so a running panel is not disturbed:
   `SPIKELOADER_PORT=4101 SPIKELOADER_UI_PORT=4111 bin/spikeloader web start` (background).
3. Create a throwaway repo with a pipeline that has two stages, an artifact, a cache, a matrix and a
   secret (see docs/backend.md › Tests), then with `SPIKELOADER_PORT=4101`:
   `bin/spikeloader ci add demo file:///…`, set the secret, `bin/spikeloader ci run demo -f` twice
   (the second run must restore the cache), and check the secret is printed as `***`.
4. Restart the daemon and confirm runs and secrets are still listed.
5. Clean up: `bin/spikeloader ci rm demo`, stop the daemon (by PID, not `pkill -f`).

Report each step's outcome; if something fails, show the output and the likely cause before fixing.
