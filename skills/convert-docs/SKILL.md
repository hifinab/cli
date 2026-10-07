---
name: convert-docs
description: Convert documents between markdown, Word (.docx), HTML, plain text, and other formats with pandoc. Use to turn a markdown report into a .docx, read a .docx or HTML file as markdown, or join several markdown files into one document.
---

# Convert with pandoc

```sh
pandoc report.md -o report.docx                       # markdown to Word
pandoc report.md --toc -o report.docx                 # with a table of contents
pandoc input.docx -t gfm -o input.md                  # Word to markdown
pandoc page.html -t gfm -o page.md                    # HTML to markdown
pandoc 01-intro.md 02-findings.md -o report.docx      # several files into one
pandoc report.md -s -o report.html --metadata title="Report"
```

- Writing markdown and converting it with pandoc is often the quickest way
  to a good-looking .docx: headings, lists, tables, links, and images
  (`![caption](chart.png)`) all carry over.
- `--reference-doc=style.docx` uses another document's styles, when the
  task gives a template.
- PDF output needs LaTeX, which isn't installed; write .docx or HTML
  instead and say so.
- Check the output: convert it back to markdown, or open it with the
  word-docs skill, and look at headings and tables.
