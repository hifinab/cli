#!/usr/bin/env bash
# Runs on a remote instance, started by `hi compute serve`.
# Idempotent: builds or reuses llama.cpp, downloads a GGUF quant, and starts
# llama-server, skipping whatever is already done. The current step is kept
# in ~/.hi/state (read over SSH) and printed as `hi-state: ...` lines (read
# from provider logs).
#
# Env: REPO QUANT CTX PORT HOST ALIAS EXTRA_ARGS HI_FOREGROUND

set -uo pipefail

REPO=${REPO:?REPO is required}
QUANT=${QUANT:?QUANT is required}
CTX=${CTX:-32768}
PORT=${PORT:-8000}
HOST=${HOST:-127.0.0.1}
ALIAS=${ALIAS:-model}
EXTRA_ARGS=${EXTRA_ARGS:-}
# In a job whose command is this script, keep running while the server runs.
HI_FOREGROUND=${HI_FOREGROUND:-0}

# SSH sessions do not inherit the notebook kernel's environment; on Colab the
# NVIDIA driver libraries live in /usr/lib64-nvidia.
for dir in /usr/lib64-nvidia /usr/local/nvidia/lib64; do
  [[ -d $dir ]] && export LD_LIBRARY_PATH=$dir${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}
done
export PATH=$PATH:/usr/local/cuda/bin

HI=$HOME/.hi
LOGS=$HI/logs
# Colab's large disk is /content; elsewhere keep everything under ~/.hi.
if [[ -d /content ]]; then WORK=/content/hi; else WORK=$HI; fi
LLAMA=$WORK/llama.cpp
MODELS=$WORK/models/${REPO//\//--}
mkdir -p "$LOGS" "$MODELS"

state() {
  echo "$*" > "$HI/state"
  echo "[$(date +%T)] $*" >> "$LOGS/serve.log"
  echo "hi-state: $*"
}
# Failures also print the end of the relevant log, so provider logs show why.
fail() {
  local log=${2:-}
  [[ -n $log && -f $log ]] && tail -n 30 "$log"
  state "failed: $1"
  exit 1
}

# curl is missing from some images; fall back to bash's /dev/tcp.
healthy() {
  if command -v curl >/dev/null; then
    curl -sf "localhost:$PORT/health" >/dev/null
  else
    { exec 3<>"/dev/tcp/127.0.0.1/$PORT" &&
      printf 'GET /health HTTP/1.0\r\nHost: localhost\r\n\r\n' >&3 &&
      head -1 <&3 | grep -q ' 200'; } 2>/dev/null
  fi
}

if healthy; then state "ready"; exit 0; fi

# Prefer a prebuilt server, such as the one in llama.cpp's own images.
server=""
for candidate in /app/llama-server "$(command -v llama-server 2>/dev/null)" "$LLAMA/build/bin/llama-server"; do
  [[ -n $candidate && -x $candidate ]] && { server=$candidate; break; }
done

# A prebuilt llama-server downloads the model itself with -hf. Otherwise
# llama.cpp is built here, and the hf CLI downloads in parallel.
if [[ -n $server ]]; then download=server; else download=hf; fi

(
  if [[ -z $server ]]; then
    cuda=OFF
    command -v nvcc >/dev/null && cuda=ON
    { rm -rf "$LLAMA" &&
      git clone --depth 1 https://github.com/ggml-org/llama.cpp "$LLAMA" &&
      cmake -S "$LLAMA" -B "$LLAMA/build" -DGGML_CUDA=$cuda -DCMAKE_CUDA_ARCHITECTURES=native -DLLAMA_CURL=OFF &&
      cmake --build "$LLAMA/build" -j "$(nproc)" --target llama-server; } > "$LOGS/build.log" 2>&1
  fi
) &
build_pid=$!

# hf download skips complete files and resumes partial ones, so always run it.
if [[ $download == hf ]]; then
  (
    python3 -m pip install -q -U "huggingface_hub[hf_xet]" > "$LOGS/pip.log" 2>&1
    HF_XET_HIGH_PERFORMANCE=1 hf download "$REPO" --include "*${QUANT}*.gguf" \
      --local-dir "$MODELS" > "$LOGS/download.log" 2>&1
  ) &
  download_pid=$!
fi

if [[ -z $server ]]; then
  state "building llama.cpp and downloading $REPO:$QUANT"
  wait $build_pid || fail "llama.cpp build (see $LOGS/build.log)" "$LOGS/build.log"
  server=$LLAMA/build/bin/llama-server
  state "llama.cpp built, downloading $REPO:$QUANT"
fi

if [[ $download == hf ]]; then
  [[ -n ${download_pid:-} ]] && state "downloading $REPO:$QUANT"
  wait "$download_pid" || fail "download (see $LOGS/download.log)" "$LOGS/download.log"
  model=$(find "$MODELS" -name "*${QUANT}*-00001-of-*.gguf" | sort | head -1)
  [[ -n $model ]] || model=$(find "$MODELS" -name "*${QUANT}*.gguf" | sort | head -1)
  [[ -n $model ]] || fail "no $QUANT .gguf file in $REPO"
  model_args=(-m "$model")
  state "loading model"
  ready_within=600
else
  model_args=(-hf "$REPO:$QUANT")
  state "downloading and loading $REPO:$QUANT"
  ready_within=3600
fi

# shellcheck disable=SC2086
"$server" "${model_args[@]}" \
  --host "$HOST" --port "$PORT" --alias "$ALIAS" \
  -ngl 999 -c "$CTX" -np 1 -fa on --jinja $EXTRA_ARGS \
  > "$LOGS/server.log" 2>&1 &
server_pid=$!

for ((waited = 0; waited < ready_within; waited += 5)); do
  sleep 5
  if healthy; then
    state "ready"
    if [[ $HI_FOREGROUND == 1 ]]; then
      wait $server_pid
      status=$?
      tail -n 20 "$LOGS/server.log"
      exit $status
    fi
    exit 0
  fi
  if ! kill -0 $server_pid 2>/dev/null; then
    fail "llama-server exited (see $LOGS/server.log)" "$LOGS/server.log"
  fi
done
fail "llama-server not ready after $((ready_within / 60)) minutes (see $LOGS/server.log)" "$LOGS/server.log"
