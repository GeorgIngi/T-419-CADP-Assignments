# Verify the output of a the Voluspa dwarf announcement program written in Go.
import subprocess
from collections import Counter

DWARVES = [
    "Þorinn", "Balin", "Bífurr", "Báfurr", "Bömburr", "Dóri", "Dvalinn",
    "Fíli", "Glóinn", "Kíli", "Nóri", "Þrainn", "Óri", "Gandalfr"
]

PREFIX = "Hi! My name is "


def verify(output: str) -> bool:
    # Split into non-empty lines (strip trailing newline)
    lines = [ln.strip() for ln in output.splitlines() if ln.strip() != ""]

    # Must be exactly 14 announcements
    if len(lines) != len(DWARVES):
        return False

    names = []
    for ln in lines:
        if not ln.startswith(PREFIX):
            return False
        name = ln[len(PREFIX):]
        names.append(name)

    # Must match exactly the multiset of dwarf names (no missing/extra/duplicates)
    return Counter(names) == Counter(DWARVES)


def main():
    # Option A: run via `go run .` (requires go.mod) or `go run voluspa.go`
    cmd = ["go", "run", "voluspa.go"]

    try:
        proc = subprocess.run(
            cmd,
            capture_output=True,
            text=True,          # decode to str
            encoding="utf-8",   # important for Þ, Ó, etc.
            timeout=10
        )
    except Exception:
        print("No")
        return

    # Must exit successfully
    if proc.returncode != 0:
        print("No")
        return

    print("Yes" if verify(proc.stdout) else "No")


if __name__ == "__main__":
    main()