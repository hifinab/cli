import math

from hifin_template_name.config import TrainConfig
from hifin_template_name.train import train


def test_two_training_steps_on_the_cpu() -> None:
    losses = train(TrainConfig(steps=2, device="cpu"))
    assert len(losses) == 2
    assert all(math.isfinite(loss) for loss in losses)


def test_training_is_reproducible() -> None:
    config = TrainConfig(steps=3, device="cpu")
    assert train(config) == train(config)


def test_training_learns() -> None:
    losses = train(TrainConfig(steps=200, device="cpu"))
    assert losses[-1] < losses[0] / 10
