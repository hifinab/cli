---
title: The hi guide
description: Step-by-step guides for hi, the Hifin command-line tool. Set up a workstation, then rent GPUs on Colab or Hugging Face to run jobs, open shells, and serve models.
---

`hi` does two jobs. It sets up Hifin workstations, and its `hi compute`
commands rent remote machines so you can use a GPU you do not have: run a
training script, open a shell, forward a port to your laptop, or serve a model
with an OpenAI-compatible API. The same commands work on Google Colab and
Hugging Face.

## Where to start

<div class="cards">
  <a href="{{ '/guide/install/' | relative_url }}"><strong>Install hi</strong><span>One command on Linux, then sign in to a provider.</span></a>
  <a href="{{ '/guide/quickstart/' | relative_url }}"><strong>Your first remote GPU</strong><span>Five minutes from install to a shell on a rented GPU.</span></a>
  <a href="{{ '/guide/compute/' | relative_url }}"><strong>How hi compute works</strong><span>Providers, runs and instances, hardware names, and limits.</span></a>
  <a href="{{ '/guide/examples/big-model/' | relative_url }}"><strong>Examples</strong><span>Serve a 125B model, train, sweep, JupyterLab, and agents.</span></a>
</div>

## What you can do

| I want to…                                         | Read                                                              |
|----------------------------------------------------|-------------------------------------------------------------------|
| Run a Python script or container on a GPU          | [Run a job to completion]({{ '/guide/compute/run/' | relative_url }}) |
| Get a shell on a GPU machine and forward a port    | [Interactive machines]({{ '/guide/compute/instances/' | relative_url }}) |
| Try an LLM on hardware I do not have               | [Serve a model]({{ '/guide/compute/serve/' | relative_url }})       |
| Download the team's private datasets and models    | [Download the team's data]({{ '/guide/data/' | relative_url }}) |
| Give a cloud job the team's data                   | [Run a job: the team's data]({{ '/guide/compute/run/#the-teams-data' | relative_url }}) |
| Know what I am paying and who pays                 | [Costs, limits, and billing]({{ '/guide/compute/billing/' | relative_url }}) |
| Set up Colab or Hugging Face for the first time    | [Google Colab]({{ '/guide/compute/colab/' | relative_url }}), [Hugging Face Jobs]({{ '/guide/compute/hugging-face/' | relative_url }}) |
| Let Claude Code or Codex use a GPU safely          | [Let your coding agent use a GPU]({{ '/guide/examples/agent/' | relative_url }}) |
| Turn "move the md files to notes" into a command   | [Ask for a command]({{ '/guide/q/' | relative_url }})              |
| Start a new repository that agents work well in   | [Start a project]({{ '/guide/projects/' | relative_url }})          |
| Set up a new Hifin machine                         | [Set up a workstation]({{ '/guide/workstation/' | relative_url }}) |
| Fix an error message                               | [Troubleshooting]({{ '/guide/reference/troubleshooting/' | relative_url }}) |

## Conventions

Commands are shown as you type them. Replace names in angle brackets, such as
`<name>`, with your own. `hi help` lists every command, and `hi compute help`
lists the compute commands and their options.

> Commands that rent hardware cost money. Each one prints the price before it
> starts (and on Hugging Face, who pays), and asks you to confirm paid
> hardware. Add `--dry-run` to see what
> would happen without starting anything.
{: .note}

Press <kbd>/</kbd> anywhere to search the guide.
