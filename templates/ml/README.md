# Hifin Template Name

Model training with PyTorch.

```sh
uv sync                                  # CPU build of PyTorch
uv sync --no-group cpu --group rocm      # AMD Strix Halo (gfx1151), from AMD's index
uv sync --no-group cpu --group cuda      # NVIDIA GPUs
make check                               # includes a two-step training run on the CPU
uv run python -m hifin_template_name.train --steps 500
```

The `rocm` group installs AMD's PyTorch build for gfx1151, which ships its
own ROCm libraries, so it needs only the GPU driver that `hi install strix`
sets up. For another AMD GPU, change the `pytorch-rocm` index in
`pyproject.toml` to its folder at https://repo.amd.com/rocm/whl/.

To train on a rented GPU, start a machine with `hi compute up`, copy the
project over, and run the same command there with `--group cuda`.
