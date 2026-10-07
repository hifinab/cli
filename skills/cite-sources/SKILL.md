---
name: cite-sources
description: How to record where each fact comes from when researching on the web and writing findings to files. Use for any research, comparison, or summary that relies on web pages or documents.
---

# Cite sources

Every fact you write down from a source gets its source next to it, so a
reader can check it.

- Keep a running `sources.md` in the working folder while you research: one
  line per source with a short id, the title, the URL, the publisher, the
  page's date if it has one, and the date you read it.

  ```markdown
  - [runpod-pricing] RunPod GPU pricing, https://www.runpod.io/pricing, RunPod, read 2026-10-07
  ```

- In the results, cite with the id in brackets after the fact:
  "RunPod rents an H100 SXM at $2.69/h [runpod-pricing]." In CSV or Excel
  output, add a `source` column with the URL.
- Prefer primary sources: the company's own pricing page, filing, or
  documentation over a blog post about it. Say when only secondary sources
  were found.
- Quote numbers exactly as the source gives them, with currency, unit, and
  date. Write "not published" rather than estimate, unless the task asks for
  estimates; then mark them as estimates and say how you made them.
- When sources disagree, give both with their dates and say which you used.
- End the document with a "Sources" section built from `sources.md`.
