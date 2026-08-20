#!/usr/bin/env python3

from __future__ import annotations

import sys
from pathlib import Path

SCRIPT_DIR = Path(__file__).resolve().parent
sys.path.insert(0, str(SCRIPT_DIR))

from tunnel_mcp_installer.cli import main

if __name__ == "__main__":
    raise SystemExit(main())
