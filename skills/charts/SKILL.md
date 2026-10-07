---
name: charts
description: Draw clear, labelled charts with matplotlib and save them as PNG or SVG files. Use when a task asks for a chart, plot, or figure, or when a report would be clearer with one.
---

# Charts with matplotlib

There's no screen in the box; matplotlib draws to files.

```python
import matplotlib.pyplot as plt

fig, ax = plt.subplots(figsize=(8, 4.5), layout="constrained")
ax.plot(daily["date"], daily["volume"] / 1e6, linewidth=1.5)
ax.set_title("Daily traded volume")
ax.set_xlabel("Date")
ax.set_ylabel("Volume (millions of shares)")
ax.grid(alpha=0.3)
fig.savefig("volume.png", dpi=150)
plt.close(fig)
```

- One message per chart, said in the title. Label both axes with units, and
  start bar charts at zero.
- Bar charts for comparing categories (sorted by value), lines for time,
  scatter for two measures. No pie charts with more than a few slices, and
  no 3D.
- Use matplotlib's default colours, and a legend only when there is more
  than one series.
- Save PNG at 150 dpi for documents, SVG when the reader may scale it. Name
  files after what they show (`h100-price-by-provider.png`).
- Look at the result: open the PNG and check that labels aren't cut off or
  overlapping before you put it in a report.
