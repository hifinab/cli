"""The model and how it trains: the only file agents change during a run.

train(data, vocab_size, block_size, budget, device) gets the training text as
character ids and returns a model, trained for at most budget seconds.
evaluate.py then calls model(ids) with ids of shape (batch, block_size), which
must return logits of shape (batch, block_size, vocab_size).

The example is a small GPT, character by character, in the style of
nanoGPT: pre-norm transformer blocks with causal attention, AdamW, and a
learning rate that warms up and then decays over the time budget. Replace it
with your own model before a run.
"""

import math
import time
from dataclasses import dataclass

import torch
from torch import nn
from torch.nn import functional


@dataclass(frozen=True)
class Config:
    layers: int = 4
    heads: int = 4
    width: int = 128
    batch_size: int = 32
    learning_rate: float = 1e-3
    warmup: float = 0.05  # of the budget
    weight_decay: float = 0.1


class Attention(nn.Module):
    def __init__(self, width: int, heads: int) -> None:
        super().__init__()
        self.heads = heads
        self.qkv = nn.Linear(width, 3 * width)
        self.out = nn.Linear(width, width)

    def forward(self, x: torch.Tensor) -> torch.Tensor:
        batch, length, width = x.shape
        q, k, v = self.qkv(x).split(width, dim=2)
        shape = (batch, length, self.heads, width // self.heads)
        q, k, v = (t.view(shape).transpose(1, 2) for t in (q, k, v))
        y = functional.scaled_dot_product_attention(q, k, v, is_causal=True)
        return self.out(y.transpose(1, 2).reshape(batch, length, width))


class Block(nn.Module):
    def __init__(self, width: int, heads: int) -> None:
        super().__init__()
        self.norm1 = nn.LayerNorm(width)
        self.attention = Attention(width, heads)
        self.norm2 = nn.LayerNorm(width)
        self.mlp = nn.Sequential(
            nn.Linear(width, 4 * width), nn.GELU(), nn.Linear(4 * width, width)
        )

    def forward(self, x: torch.Tensor) -> torch.Tensor:
        x = x + self.attention(self.norm1(x))
        return x + self.mlp(self.norm2(x))


class GPT(nn.Module):
    def __init__(self, vocab_size: int, block_size: int, config: Config) -> None:
        super().__init__()
        self.tokens = nn.Embedding(vocab_size, config.width)
        self.positions = nn.Embedding(block_size, config.width)
        self.blocks = nn.Sequential(
            *(Block(config.width, config.heads) for _ in range(config.layers))
        )
        self.norm = nn.LayerNorm(config.width)
        self.head = nn.Linear(config.width, vocab_size)

    def forward(self, ids: torch.Tensor) -> torch.Tensor:
        positions = torch.arange(ids.shape[1], device=ids.device)
        x = self.tokens(ids) + self.positions(positions)
        return self.head(self.norm(self.blocks(x)))


def learning_rate(config: Config, fraction: float) -> float:
    """Linear warmup, then cosine decay to zero at the end of the budget."""
    if fraction < config.warmup:
        return config.learning_rate * fraction / config.warmup
    progress = (fraction - config.warmup) / (1 - config.warmup)
    return config.learning_rate * 0.5 * (1 + math.cos(math.pi * min(progress, 1.0)))


def train(
    data: torch.Tensor, vocab_size: int, block_size: int, budget: float, device: torch.device
) -> nn.Module:
    config = Config()
    torch.manual_seed(0)
    model = GPT(vocab_size, block_size, config).to(device)
    optimizer = torch.optim.AdamW(
        model.parameters(),
        lr=config.learning_rate,
        betas=(0.9, 0.95),
        weight_decay=config.weight_decay,
    )
    generator = torch.Generator().manual_seed(0)
    model.train()
    start = time.perf_counter()
    while (elapsed := time.perf_counter() - start) < budget:
        for group in optimizer.param_groups:
            group["lr"] = learning_rate(config, elapsed / budget)
        offsets = torch.randint(
            len(data) - block_size - 1, (config.batch_size,), generator=generator
        )
        inputs = torch.stack([data[i : i + block_size] for i in offsets.tolist()]).to(device)
        targets = torch.stack([data[i + 1 : i + block_size + 1] for i in offsets.tolist()]).to(
            device
        )
        logits = model(inputs)
        loss = functional.cross_entropy(logits.reshape(-1, vocab_size), targets.reshape(-1))
        optimizer.zero_grad(set_to_none=True)
        loss.backward()
        nn.utils.clip_grad_norm_(model.parameters(), 1.0)
        optimizer.step()
    return model
