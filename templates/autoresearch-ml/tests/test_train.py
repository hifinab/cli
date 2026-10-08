import math

import torch

import prepare
import train

CPU = torch.device("cpu")


def test_the_example_model_learns_in_seconds(monkeypatch) -> None:
    # A short context so the test is quick; the real score uses prepare.BLOCK.
    monkeypatch.setattr(prepare, "BLOCK", 32)
    vocabulary = prepare.Vocabulary("abcdefgh")
    data = vocabulary.encode("abcdefgh" * 400)
    model = train.train(data, len(vocabulary), 32, 3.0, CPU)
    assert model(data[:32].view(1, 32)).shape == (1, 32, 8)
    bpc = prepare.bits_per_char(model, data[:1025], len(vocabulary), CPU)
    assert bpc < math.log2(len(vocabulary)) / 3  # far better than guessing


def test_the_learning_rate_warms_up_and_decays() -> None:
    config = train.Config()
    assert train.learning_rate(config, 0.0) == 0.0
    assert train.learning_rate(config, config.warmup) == config.learning_rate
    assert train.learning_rate(config, 1.0) < 1e-12
