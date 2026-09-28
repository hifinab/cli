// The guide's behaviour: search, "On this page", heading anchors, copy
// buttons, and the mobile menu. No dependencies.
(function () {
  "use strict";

  // Heading anchors and the "On this page" list.
  const toc = document.getElementById("toc");
  const headings = Array.from(document.querySelectorAll(".prose h2[id], .prose h3[id]"));
  headings.forEach(function (heading) {
    const anchor = document.createElement("a");
    anchor.className = "anchor";
    anchor.href = "#" + heading.id;
    anchor.setAttribute("aria-label", "Link to this section");
    anchor.textContent = "#";
    heading.appendChild(anchor);
    if (toc) {
      const item = document.createElement("li");
      if (heading.tagName === "H3") item.className = "sub";
      const link = document.createElement("a");
      link.href = "#" + heading.id;
      link.textContent = heading.firstChild.textContent;
      item.appendChild(link);
      toc.appendChild(item);
    }
  });
  if (toc && headings.length === 0) toc.parentElement.hidden = true;
  if (toc && "IntersectionObserver" in window) {
    const links = new Map(Array.from(toc.querySelectorAll("a")).map(function (a) {
      return [a.getAttribute("href").slice(1), a];
    }));
    const observer = new IntersectionObserver(function (entries) {
      entries.forEach(function (entry) {
        if (!entry.isIntersecting) return;
        links.forEach(function (a) { a.classList.remove("active"); });
        const active = links.get(entry.target.id);
        if (active) active.classList.add("active");
      });
    }, { rootMargin: "-70px 0px -70% 0px" });
    headings.forEach(function (heading) { observer.observe(heading); });
  }

  // Copy buttons on code blocks.
  document.querySelectorAll(".prose div.highlighter-rouge").forEach(function (block) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "copy";
    button.textContent = "copy";
    button.addEventListener("click", function () {
      const code = block.querySelector("code");
      navigator.clipboard.writeText(code ? code.innerText.replace(/\n$/, "") : "").then(function () {
        button.textContent = "copied";
      }, function () {
        button.textContent = "select + copy";
      });
      setTimeout(function () { button.textContent = "copy"; }, 1500);
    });
    block.appendChild(button);
  });

  // Mobile menu.
  const menu = document.querySelector(".menu-button");
  if (menu) {
    menu.addEventListener("click", function () {
      const open = document.body.classList.toggle("nav-open");
      menu.setAttribute("aria-expanded", String(open));
    });
    document.querySelectorAll(".sidebar a").forEach(function (a) {
      a.addEventListener("click", function () { document.body.classList.remove("nav-open"); });
    });
  }

  // Search over an index Jekyll builds from every guide page.
  const input = document.getElementById("search");
  const results = document.getElementById("results");
  if (!input || !results) return;
  let index = null;
  let loading = null;
  let selected = -1;

  function load() {
    if (index) return Promise.resolve(index);
    if (!loading) {
      loading = fetch(window.GUIDE_INDEX).then(function (r) { return r.json(); }).then(function (pages) {
        index = pages.map(function (p) {
          p.title = decode(p.title);
          p.text = decode(p.text);
          p.description = decode(p.description || "");
          p.headings = (p.headings || []).map(decode);
          return Object.assign(p, {
            lowerTitle: p.title.toLowerCase(),
            lowerText: p.text.toLowerCase(),
            lowerHeadings: (p.headings || []).map(function (h) { return h.toLowerCase(); })
          });
        });
        return index;
      });
    }
    return loading;
  }

  // The index holds text with HTML entities such as &lt;name&gt;.
  const decoder = document.createElement("textarea");
  function decode(text) {
    decoder.innerHTML = text;
    return decoder.value;
  }

  function escapeHTML(text) {
    return text.replace(/[&<>"]/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", "\"": "&quot;" }[c];
    });
  }

  function highlight(text, terms) {
    let html = escapeHTML(text);
    terms.forEach(function (term) {
      if (!term) return;
      const pattern = new RegExp("(" + term.replace(/[.*+?^${}()|[\]\\]/g, "\\$&") + ")", "ig");
      html = html.replace(pattern, "<mark>$1</mark>");
    });
    return html;
  }

  function snippet(page, terms) {
    const at = page.lowerText.indexOf(terms[0]);
    if (at < 0) return page.description || page.text.slice(0, 140);
    const start = Math.max(0, at - 50);
    return (start > 0 ? "…" : "") + page.text.slice(start, at + 110).trim() + "…";
  }

  function search(query) {
    const phrase = query.toLowerCase().trim();
    const terms = phrase.split(/\s+/).filter(Boolean);
    if (terms.length === 0) return [];
    const found = [];
    index.forEach(function (page) {
      let score = 0;
      let matched = 0;
      let heading = null;
      terms.forEach(function (term) {
        let termScore = 0;
        if (page.lowerTitle.includes(term)) termScore += 10;
        page.lowerHeadings.forEach(function (h, i) {
          if (h.includes(term)) {
            termScore += 4;
            if (heading === null) heading = { text: page.headings[i], id: (page.ids || [])[i] };
          }
        });
        termScore += Math.min(page.lowerText.split(term).length - 1, 6);
        if (termScore > 0) matched++;
        score += termScore;
      });
      if (matched === 0) return;
      // The whole query as a phrase counts most, in a heading above all.
      if (terms.length > 1) {
        page.lowerHeadings.forEach(function (h, i) {
          if (h.includes(phrase)) {
            score += 20;
            heading = { text: page.headings[i], id: (page.ids || [])[i] };
          }
        });
        if (page.lowerTitle.includes(phrase)) score += 20;
        if (page.lowerText.includes(phrase)) score += 5;
      }
      found.push({ page: page, score: score, matched: matched, heading: heading });
    });
    // Pages matching every word come first; the rest are a fallback.
    found.sort(function (a, b) { return b.matched - a.matched || b.score - a.score; });
    const best = found.length ? found[0].matched : 0;
    return found.filter(function (hit) { return hit.matched === best; }).slice(0, 8)
      .map(function (hit) { hit.terms = terms; return hit; });
  }

  function render(hits, query) {
    selected = -1;
    results.innerHTML = "";
    if (!query) {
      results.hidden = true;
      input.setAttribute("aria-expanded", "false");
      return;
    }
    if (hits.length === 0) {
      results.innerHTML = '<li class="r-empty">No results for “' + escapeHTML(query) + '”.</li>';
    }
    hits.forEach(function (hit, i) {
      const item = document.createElement("li");
      item.setAttribute("role", "option");
      item.id = "result-" + i;
      const url = hit.page.url + (hit.heading && hit.heading.id ? "#" + hit.heading.id : "");
      const where = hit.page.section + (hit.heading ? " › " + hit.heading.text : "");
      item.innerHTML =
        '<a href="' + url + '"><span class="r-where">' + escapeHTML(where) + "</span>" +
        '<span class="r-title">' + highlight(hit.page.title, hit.terms) + "</span>" +
        '<span class="r-snippet">' + highlight(snippet(hit.page, hit.terms), hit.terms) + "</span></a>";
      results.appendChild(item);
    });
    results.hidden = false;
    input.setAttribute("aria-expanded", "true");
  }

  function move(delta) {
    const items = results.querySelectorAll("li[role=option]");
    if (items.length === 0) return;
    if (selected >= 0) items[selected].removeAttribute("aria-selected");
    selected = (selected + delta + items.length) % items.length;
    items[selected].setAttribute("aria-selected", "true");
    items[selected].scrollIntoView({ block: "nearest" });
    input.setAttribute("aria-activedescendant", items[selected].id);
  }

  input.addEventListener("focus", load);
  input.addEventListener("input", function () {
    const query = input.value.trim();
    load().then(function () { render(search(query), query); });
  });
  input.addEventListener("keydown", function (event) {
    if (event.key === "ArrowDown") { event.preventDefault(); move(1); }
    else if (event.key === "ArrowUp") { event.preventDefault(); move(-1); }
    else if (event.key === "Enter") {
      const items = results.querySelectorAll("li[role=option] a");
      const target = items[selected >= 0 ? selected : 0];
      if (target) { event.preventDefault(); window.location.href = target.getAttribute("href"); }
    } else if (event.key === "Escape") {
      input.value = "";
      render([], "");
      input.blur();
    }
  });
  document.addEventListener("keydown", function (event) {
    const typing = /INPUT|TEXTAREA|SELECT/.test(document.activeElement.tagName);
    if (event.key === "/" && !typing) { event.preventDefault(); input.focus(); }
  });
  document.addEventListener("click", function (event) {
    if (!event.target.closest(".search")) render([], "");
  });

  // ?q=... opens the search with a query, for links from elsewhere.
  const initial = new URLSearchParams(window.location.search).get("q");
  if (initial) {
    input.value = initial;
    load().then(function () { render(search(initial), initial); input.focus(); });
  }
})();
