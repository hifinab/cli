---
title: JupyterLab on a rented GPU
description: Start a GPU machine, run JupyterLab on it, and open it in your laptop's browser through a private tunnel.
---

This starts a machine, installs and starts JupyterLab on it, and forwards it
to your browser. JupyterLab listens only on the machine's localhost and is
only reachable through your tunnel.

## 1. Start a machine

```sh
hi compute up --gpu T4 --name lab --max 3h
```

On Hugging Face, use `--gpu t4-small` and make sure your
[SSH key is on the Hub](/guide/compute/hugging-face/#add-your-ssh-key-to-the-hub).

## 2. Start JupyterLab on it

```sh
hi compute ssh lab -- 'pip install -q jupyterlab &&
  (setsid nohup jupyter lab --no-browser --allow-root \
     --ip 127.0.0.1 --port 8888 --IdentityProvider.token="" \
     > /tmp/jupyter.log 2>&1 < /dev/null &) && sleep 5 && tail -3 /tmp/jupyter.log'
```

- `setsid nohup … &` keeps JupyterLab running after the SSH command returns.
- `--ip 127.0.0.1` keeps it private to the machine; the tunnel is the only
  way in, so it runs without a token.

## 3. Open it

```sh
hi compute tunnel lab 8888:18888
```

Open [http://127.0.0.1:18888/lab](http://127.0.0.1:18888/lab). Using a local
port other than 8888 avoids clashing with a Jupyter already running on your
laptop.

Check the GPU from a notebook cell:

```python
!nvidia-smi
```

## 4. Keep your work

Files live on the machine's disk and disappear when it stops. Download
notebooks from JupyterLab's file browser, or copy them with `scp` through the
[SSH config entry](/guide/compute/instances/#connect-vs-code-rsync-or-plain-ssh)
on Colab.

## 5. Stop

Press Ctrl+C in the tunnel terminal and answer `y`, or:

```sh
hi compute stop lab
```

The tunnel closes by itself when the machine stops.
