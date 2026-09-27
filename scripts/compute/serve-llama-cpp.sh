#!/usr/bin/env bash
# Runs on a remote instance, uploaded and started by `hi compute serve`.
# Idempotent: builds llama.cpp, downloads a GGUF quant, and starts
# llama-server, skipping whatever is already done. The current step is kept
# in ~/.hi/state and logs in ~/.hi/logs/, which `hi` polls over SSH.
#
# Env: REPO QUANT CTX PORT ALIAS EXTRA_ARGS

set -uo pipefail

REPO=${REPO:?REPO is required}
QUANT=${QUANT:?QUANT is required}
CTX=${CTX:-32768}
PORT=${PORT:-8000}
ALIAS=${ALIAS:-model}
EXTRA_ARGS=${EXTRA_ARGS:-}

HI=$HOME/.hi
LOGS=$HI/logs
# Colab's large disk is /content; elsewhere keep everything under ~/.hi.
if [[ -d /content ]]; then WORK=/content/hi; else WORK=$HI; fi
LLAMA=$WORK/llama.cpp
MODELS=$WORK/models/${REPO//\//--}
mkdir -p "$LOGS" "$MODELS"

state() { echo "$*" > "$HI/state"; echo "[$(date +%T)] $*" >> "$LOGS/serve.log"; }
fail() { state "failed: $*"; exit 1; }

if curl -sf "localhost:$PORT/health" >/dev/null; then state "ready"; exit 0; fi

# Build and download in parallel; both take a few minutes.
(
  if [[ ! -x $LLAMA/build/bin/llama-server ]]; then
    cuda=OFF
    command -v nvcc >/dev/null || [[ -x /usr/local/cuda/bin/nvcc ]] && cuda=ON
    export PATH=$PATH:/usr/local/cuda/bin
    { rm -rf "$LLAMA" &&
      git clone --depth 1 https://github.com/ggml-org/llama.cpp "$LLAMA" &&
      cmake -S "$LLAMA" -B "$LLAMA/build" -DGGML_CUDA=$cuda -DCMAKE_CUDA_ARCHITECTURES=native -DLLAMA_CURL=OFF &&
      cmake --build "$LLAMA/build" -j "$(nproc)" --target llama-server; } > "$LOGS/build.log" 2>&1
  fi
) &
build_pid=$!

# hf download skips complete files and resumes partial ones, so always run it.
(
  python3 -m pip install -q -U "huggingface_hub[hf_xet]" > "$LOGS/pip.log" 2>&1
  HF_XET_HIGH_PERFORMANCE=1 hf download "$REPO" --include "*${QUANT}*.gguf" \
    --local-dir "$MODELS" > "$LOGS/download.log" 2>&1
) &
download_pid=$!

state "building llama.cpp and downloading $REPO:$QUANT"
wait $build_pid || fail "llama.cpp build (see $LOGS/build.log)"
state "llama.cpp built, downloading $REPO:$QUANT"
wait $download_pid || fail "download (see $LOGS/download.log)"

model=$(find "$MODELS" -name "*${QUANT}*-00001-of-*.gguf" | sort | head -1)
[[ -n $model ]] || model=$(find "$MODELS" -name "*${QUANT}*.gguf" | sort | head -1)
[[ -n $model ]] || fail "no $QUANT .gguf file in $REPO"

state "loading model"
# shellcheck disable=SC2086
nohup "$LLAMA/build/bin/llama-server" -m "$model" \
  --host 127.0.0.1 --port "$PORT" --alias "$ALIAS" \
  -ngl 999 -c "$CTX" -np 1 -fa on --jinja $EXTRA_ARGS \
  > "$LOGS/server.log" 2>&1 &

for _ in $(seq 1 120); do
  sleep 5
  curl -sf "localhost:$PORT/health" >/dev/null && { state "ready"; exit 0; }
  pgrep -f "llama-server" >/dev/null || fail "llama-server exited (see $LOGS/server.log)"
done
fail "llama-server not ready after 10 minutes (see $LOGS/server.log)"
