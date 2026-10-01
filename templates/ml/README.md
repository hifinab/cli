# Hifin Template Name

Model training with PyTorch.

```sh
make sync      # dependencies, with the PyTorch build for this machine
make check     # includes a two-step training run on the CPU
make train STEPS=500
```

`make` picks the PyTorch build: `rocm` on a Strix Halo (gfx1151), `cuda`
with an NVIDIA GPU, and `cpu` otherwise; `make help` shows which. To choose
another, put `TORCH = cpu` (or `cuda`, `rocm`) in `local.mk`, which Git
ignores. Run Python through `make` or with the same groups, such as
`uv run --no-default-groups --group dev --group rocm python ...`: a plain
`uv run` switches back to the CPU build.

The `rocm` build is AMD's PyTorch for gfx1151, which ships its
own ROCm libraries, so it needs only the GPU driver that `hi install strix`
sets up. For another AMD GPU, change the `pytorch-rocm` index in
`pyproject.toml` to its folder at https://repo.amd.com/rocm/whl/.

To train on a rented GPU, start a machine with `hi compute up`, copy the
project over, and run `make sync` and `make train` there; `make` picks
`cuda` on NVIDIA machines.
