"""A small training loop to replace with the real model and data."""

import argparse
import json
from pathlib import Path

import torch
from torch import nn

from hifin_template_name.config import TrainConfig


def pick_device(name: str) -> torch.device:
    if name != "auto":
        return torch.device(name)
    # ROCm builds of PyTorch also answer through torch.cuda.
    return torch.device("cuda" if torch.cuda.is_available() else "cpu")


def synthetic_batch(
    generator: torch.Generator, size: int, device: torch.device
) -> tuple[torch.Tensor, torch.Tensor]:
    inputs = torch.randn(size, 8, generator=generator)
    targets = inputs.sum(dim=1, keepdim=True)
    return inputs.to(device), targets.to(device)


def train(config: TrainConfig) -> list[float]:
    torch.manual_seed(config.seed)
    generator = torch.Generator().manual_seed(config.seed)
    device = pick_device(config.device)
    model = nn.Sequential(nn.Linear(8, config.hidden), nn.ReLU(), nn.Linear(config.hidden, 1)).to(
        device
    )
    optimizer = torch.optim.Adam(model.parameters(), lr=config.learning_rate)
    losses = []
    for _ in range(config.steps):
        inputs, targets = synthetic_batch(generator, config.batch_size, device)
        loss = nn.functional.mse_loss(model(inputs), targets)
        optimizer.zero_grad()
        loss.backward()
        optimizer.step()
        losses.append(loss.item())
    return losses


def main() -> None:
    parser = argparse.ArgumentParser(description="Train the model.")
    parser.add_argument("--steps", type=int, default=TrainConfig.steps)
    parser.add_argument("--device", default=TrainConfig.device)
    parser.add_argument("--out", type=Path, default=Path("results/latest"))
    arguments = parser.parse_args()
    config = TrainConfig(steps=arguments.steps, device=arguments.device)
    losses = train(config)
    arguments.out.mkdir(parents=True, exist_ok=True)
    (arguments.out / "config.json").write_text(json.dumps(config.as_dict(), indent=2) + "\n")
    (arguments.out / "losses.json").write_text(json.dumps(losses) + "\n")
    device = pick_device(config.device)
    print(f"{config.steps} steps on {device}: loss {losses[0]:.3f} -> {losses[-1]:.3f}")


if __name__ == "__main__":
    main()
