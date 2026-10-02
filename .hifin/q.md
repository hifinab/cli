# Notes for hi q in the hi CLI repository

- Go, one package in the root. Check a change with `gofmt -l . && go vet ./... && go test ./...`.
- Build a local binary into the scratch or /tmp folder, never into the repository root: `go build -o /tmp/hi .`
- Releases happen by pushing a tag `vX.Y.Z` on main; GitHub Actions builds the binaries. Don't tag without being asked.
- The website is GitHub Pages from `index.html`, `guide/`, and `_layouts/`; it rebuilds on every push to main.
- Never print or commit `.env`; it holds server passwords and provider keys.
