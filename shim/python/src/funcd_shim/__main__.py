"""``python -m funcd_shim`` entrypoint — runs the runtime shim (ADR-0049)."""

from __future__ import annotations

import sys

from .shim import main

if __name__ == "__main__":
    sys.exit(main())
