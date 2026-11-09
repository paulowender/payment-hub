#!/usr/bin/env python3
"""Create local secrets without replacing existing configuration."""
from pathlib import Path
import os
import secrets

root = Path(__file__).resolve().parent.parent
for name, data in {
    ".env": f"POSTGRES_PASSWORD={secrets.token_hex(24)}\nADMIN_SECRET={secrets.token_hex(32)}\nENCRYPTION_KEY={secrets.token_hex(32)}\nPAYMENT_HUB_PORT=18080\n",
}.items():
    path = root / name
    try:
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    except FileExistsError:
        print(f"Preserved existing {name}")
        continue
    with os.fdopen(fd, "w") as file:
        file.write(data)
    print(f"Created {name}")
