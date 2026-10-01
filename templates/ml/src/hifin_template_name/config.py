"""Training settings. Every run saves the config it used next to its results."""

from dataclasses import asdict, dataclass


@dataclass(frozen=True)
class TrainConfig:
    steps: int = 200
    batch_size: int = 64
    learning_rate: float = 1e-2
    hidden: int = 32
    seed: int = 0
    # "auto" uses a GPU when PyTorch sees one (CUDA or ROCm), else the CPU.
    device: str = "auto"

    def as_dict(self) -> dict[str, object]:
        return asdict(self)
