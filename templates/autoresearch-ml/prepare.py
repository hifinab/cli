"""The data and the measurement: which text, how it is split, how long a model
trains, and how it is scored. Don't change it during a run.

Run it on this machine, not in a box, once before a run (it needs the
network):

    make data

eval/train.txt, eval/val.txt    the first 90% of the text, committed: models
                                train on the first and are scored on the second.
eval/vocab.json                 the characters, in the order of their ids.
../<folder>-holdout/test.txt    the last 10%, outside the repository, so no
                                agent sees it; make holdout scores on it, once,
                                at the end.

The example is Tiny Shakespeare (1.1 MB, public domain), modelled character
by character, as in Karpathy's char-rnn and nanoGPT. Change URL and SHA256,
or replace download(), before a run, never during one.
"""

import hashlib
import json
import math
import os
import urllib.request
from pathlib import Path

import torch
from torch.nn import functional

URL = "https://raw.githubusercontent.com/karpathy/char-rnn/master/data/tinyshakespeare/input.txt"
SHA256 = "86c4e6aa9db7c042ec79f339dcb96d42b0075e16b8fc2e86bf0ca57e2dc565ed"
SPLITS = (0.8, 0.1)  # train and val; the rest is the holdout
TIME_BUDGET = 300.0  # seconds of training for every score
BLOCK = 256  # the context length models are scored at

HERE = Path(__file__).resolve().parent
EVAL = HERE / "eval"
HOLDOUT = Path(os.environ.get("HOLDOUT") or HERE.parent / f"{HERE.name}-holdout" / "test.txt")


class Vocabulary:
    """Characters and their ids."""

    def __init__(self, characters: str) -> None:
        self.characters = characters
        self.ids = {character: i for i, character in enumerate(characters)}

    @classmethod
    def load(cls, folder: Path = EVAL) -> Vocabulary:
        try:
            return cls("".join(json.loads((folder / "vocab.json").read_text())))
        except FileNotFoundError:
            raise SystemExit(f"there is no {folder / 'vocab.json'}; make data writes it") from None

    def __len__(self) -> int:
        return len(self.characters)

    def encode(self, text: str) -> torch.Tensor:
        return torch.tensor([self.ids[character] for character in text], dtype=torch.long)

    def decode(self, ids: torch.Tensor) -> str:
        return "".join(self.characters[i] for i in ids.tolist())


def download() -> str:
    with urllib.request.urlopen(URL, timeout=60) as response:
        data = response.read()
    if hashlib.sha256(data).hexdigest() != SHA256:
        raise SystemExit(f"{URL} isn't the file SHA256 names; check it, then update SHA256")
    return data.decode("utf-8")


def save(text: str, folder: Path = EVAL, holdout: Path = HOLDOUT) -> None:
    train_end = int(len(text) * SPLITS[0])
    val_end = int(len(text) * (SPLITS[0] + SPLITS[1]))
    folder.mkdir(parents=True, exist_ok=True)
    (folder / "vocab.json").write_text(json.dumps(sorted(set(text))) + "\n")
    (folder / "train.txt").write_text(text[:train_end])
    (folder / "val.txt").write_text(text[train_end:val_end])
    holdout.parent.mkdir(parents=True, exist_ok=True)
    holdout.write_text(text[val_end:])
    print(f"{folder}: {train_end} characters to train on, {val_end - train_end} to score on")
    print(f"{holdout}: {len(text) - val_end} characters for the end")


def load(path: Path, vocabulary: Vocabulary) -> torch.Tensor:
    try:
        return vocabulary.encode(path.read_text())
    except FileNotFoundError:
        raise SystemExit(f"there is no {path}; make data writes it") from None


def bits_per_char(
    model: torch.nn.Module, data: torch.Tensor, vocab_size: int, device: torch.device
) -> float:
    """The model's average cross-entropy on data, in bits per character: the
    text cut into blocks of BLOCK characters, each predicting the next."""
    blocks = (len(data) - 1) // BLOCK
    if blocks == 0:
        raise ValueError(f"the text is shorter than one block of {BLOCK} characters")
    inputs = data[: blocks * BLOCK].view(blocks, BLOCK)
    targets = data[1 : blocks * BLOCK + 1].view(blocks, BLOCK)
    model.eval()
    total = 0.0
    with torch.no_grad():
        for start in range(0, blocks, 16):
            batch = inputs[start : start + 16].to(device)
            logits = model(batch)
            if tuple(logits.shape) != (*batch.shape, vocab_size):
                raise ValueError(
                    f"model(ids) returned shape {tuple(logits.shape)}, "
                    f"not (batch, {BLOCK}, {vocab_size})"
                )
            loss = functional.cross_entropy(
                logits.float().reshape(-1, vocab_size),
                targets[start : start + 16].to(device).reshape(-1),
                reduction="sum",
            )
            total += float(loss)
    return total / (blocks * BLOCK) / math.log(2)


if __name__ == "__main__":
    save(download())
