import argparse
from datetime import date
from pathlib import Path

from hifin_template_name.pipeline import run


def main() -> None:
    parser = argparse.ArgumentParser(description="Fetch, land, parse, and load one date.")
    subcommands = parser.add_subparsers(dest="command", required=True)
    run_parser = subcommands.add_parser("run", help="run the pipeline for one date")
    run_parser.add_argument("--date", type=date.fromisoformat, required=True)
    run_parser.add_argument("--data", type=Path, default=Path("data"))
    arguments = parser.parse_args()
    loaded = run(arguments.date, arguments.data)
    print(f"{arguments.date}: loaded {loaded} new rows")


if __name__ == "__main__":
    main()
