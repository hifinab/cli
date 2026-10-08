import math
from pathlib import Path

import pytest
import torch
from torch import nn

import prepare
from prepare import Vocabulary


class Uniform(nn.Module):
    """Predicts every character equally: exactly log2(vocab) bits each."""

    def __init__(self, vocab_size: int) -> None:
        super().__init__()
        self.vocab_size = vocab_size

    def forward(self, ids: torch.Tensor) -> torch.Tensor:
        return torch.zeros(*ids.shape, self.vocab_size)


def test_the_vocabulary_round_trips() -> None:
    vocabulary = Vocabulary("\n abc")
    ids = vocabulary.encode("cab a\n")
    assert ids.tolist() == [4, 2, 3, 1, 2, 0]
    assert vocabulary.decode(ids) == "cab a\n"


def test_a_uniform_model_scores_log2_of_the_vocabulary() -> None:
    data = torch.randint(0, 50, (prepare.BLOCK * 3 + 1,))
    assert prepare.bits_per_char(Uniform(50), data, 50, torch.device("cpu")) == pytest.approx(
        math.log2(50)
    )


def test_a_model_of_the_wrong_shape_is_refused() -> None:
    data = torch.zeros(prepare.BLOCK * 2 + 1, dtype=torch.long)
    with pytest.raises(ValueError, match="shape"):
        prepare.bits_per_char(Uniform(10), data, 12, torch.device("cpu"))


def test_the_holdout_is_kept_out_of_eval(tmp_path: Path) -> None:
    text = "".join(chr(ord("a") + i % 26) for i in range(1000))
    holdout = tmp_path / "outside" / "test.txt"
    prepare.save(text, tmp_path / "eval", holdout)
    train = (tmp_path / "eval" / "train.txt").read_text()
    val = (tmp_path / "eval" / "val.txt").read_text()
    assert (len(train), len(val), len(holdout.read_text())) == (800, 100, 100)
    assert train + val + holdout.read_text() == text
    assert len(Vocabulary.load(tmp_path / "eval")) == 26
