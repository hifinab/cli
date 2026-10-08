"""The fixed evaluation: train train.py's model for prepare.TIME_BUDGET seconds,
then measure it on text it never trained on. Don't change it during a run.

hi agent best-of scores every attempt with this file, and agents may change
only train.py, so the data, the time, and the measurement stay the same.

    python evaluate.py                  the score, on eval/val.txt
    python evaluate.py --holdout PATH   on text kept outside the repository
                                        (make holdout)

A train() that runs much past the budget fails: a longer run would win by
training longer, not better. The last line is the score: val_bpc, bits per
character, lower is better.
"""

import argparse
import importlib
import math
import re
import sys
import time
from collections.abc import Callable
from pathlib import Path

import torch

import prepare

# Bound before train.py is imported, so nothing it does changes the clock.
clock = time.perf_counter
OVERRUN = 1.05  # of the budget
GRACE = 15.0  # seconds, for setup such as moving the model to the GPU

Train = Callable[[torch.Tensor, int, int, float, torch.device], torch.nn.Module]

# train() gets its data from its argument and nothing else: no files (eval/
# holds the text it is scored on), no network, and only these modules.
ALLOWED_IMPORTS = {
    "torch",
    "math",
    "time",
    "dataclasses",
    "functools",
    "itertools",
    "collections",
    "typing",
    "__future__",
}
FORBIDDEN = [
    (r"\bopen\s*\(", "open()"),
    (r"\b(load|from_file|fromfile|read_text|read_bytes|loadtxt|save)\s*\(", "files"),
    (r"\bhub\b", "torch.hub"),
    (
        r"__import__|\bexec\s*\(|\beval\s*\(|\bcompile\s*\(|\bglobals\s*\(|\bvars\s*\(|"
        r"__builtins__|__loader__|__spec__",
        "dynamic code",
    ),
]


class EvaluationError(Exception):
    """A problem with the model or its training; the score fails with it."""


def check_source(source: str) -> None:
    """Refuse a train.py that could get data other than its argument."""
    pattern = r"^\s*(?:from\s+([\w.]+)\s+import|import\s+([\w., ]+))"
    for match in re.finditer(pattern, source, re.MULTILINE):
        if match.group(1):
            modules = [match.group(1)]
        else:
            modules = [part.strip().split(" ")[0] for part in match.group(2).split(",")]
        for module in modules:
            if module.split(".")[0] not in ALLOWED_IMPORTS:
                allowed = ", ".join(sorted(ALLOWED_IMPORTS - {"__future__"}))
                raise EvaluationError(f"train.py imports {module}; it may import only {allowed}")
    for forbidden, what in FORBIDDEN:
        if re.search(forbidden, source):
            raise EvaluationError(
                f"train.py may not use {what}: train() gets all its data from its argument"
            )


def pick_device() -> torch.device:
    # ROCm builds of PyTorch answer through torch.cuda too.
    return torch.device("cuda" if torch.cuda.is_available() else "cpu")


def run(
    train: Train,
    train_data: torch.Tensor,
    score_data: torch.Tensor,
    vocab_size: int,
    budget: float,
    device: torch.device,
    grace: float = GRACE,
) -> dict[str, float]:
    torch.manual_seed(0)
    start = clock()
    model = train(train_data, vocab_size, prepare.BLOCK, budget, device)
    seconds = clock() - start
    if seconds > budget * OVERRUN + grace:
        raise EvaluationError(f"train() took {seconds:.0f}s; the budget is {budget:.0f}s")
    if not isinstance(model, torch.nn.Module):
        raise EvaluationError(f"train() returned {type(model).__name__}, not a torch.nn.Module")
    try:
        bpc = prepare.bits_per_char(model, score_data, vocab_size, device)
    except ValueError as error:
        raise EvaluationError(str(error)) from None
    if not math.isfinite(bpc):
        raise EvaluationError("the model's predictions aren't numbers (NaN or infinite)")
    parameters = sum(parameter.numel() for parameter in model.parameters())
    return {"training_seconds": seconds, "params_m": parameters / 1e6, "bpc": bpc}


def main() -> None:
    parser = argparse.ArgumentParser(description="Train train.py's model and score it.")
    parser.add_argument("--holdout", metavar="PATH", type=Path, help="score on this text")
    arguments = parser.parse_args()
    try:
        check_source(Path("train.py").read_text())
        train = importlib.import_module("train")
        vocabulary = prepare.Vocabulary.load()
        train_data = prepare.load(prepare.EVAL / "train.txt", vocabulary)
        score_data = prepare.load(arguments.holdout or prepare.EVAL / "val.txt", vocabulary)
        device = pick_device()
        result = run(
            train.train, train_data, score_data, len(vocabulary), prepare.TIME_BUDGET, device
        )
    except EvaluationError as error:
        print(f"error: {error}")
        sys.exit(1)
    name = torch.cuda.get_device_name(device) if device.type == "cuda" else "the CPU"
    print(f"device:            {name}")
    print(f"training_seconds:  {result['training_seconds']:.1f}")
    print(f"params_m:          {result['params_m']:.2f}")
    print(f"{'test_bpc' if arguments.holdout else 'val_bpc'}:           {result['bpc']:.6f}")


if __name__ == "__main__":
    main()
