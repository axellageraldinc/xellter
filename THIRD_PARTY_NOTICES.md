# Third-party notices

## Go

| Component | Where | License |
|---|---|---|
| [modernc.org/sqlite](https://gitlab.com/cznic/sqlite) | Go dependency | BSD-3-Clause |
| [gopkg.in/yaml.v3](https://github.com/go-yaml/yaml) | Go dependency | MIT / Apache-2.0 |
| [golang.org/x/crypto](https://pkg.go.dev/golang.org/x/crypto) | Go dependency | BSD-3-Clause |

Go dependency versions are pinned in `go.mod` / `go.sum`; transitive modules carry their own licenses.

## The panel (`web/`)

| Component | Where | License |
|---|---|---|
| [React](https://react.dev) | panel runtime | MIT |
| [React Router](https://reactrouter.com) | panel routing | MIT |
| [Tailwind CSS](https://tailwindcss.com) | panel styling | MIT |
| [shadcn/ui](https://ui.shadcn.com) | panel components (the code is in `web/src/components/ui`) | MIT |
| [Radix UI](https://www.radix-ui.com) | the primitives behind the shadcn/ui components | MIT |
| [lucide](https://lucide.dev) | panel icons | ISC |
| [class-variance-authority](https://cva.style) | component variants | Apache-2.0 |
| [clsx](https://github.com/lukeed/clsx) | class names | MIT |
| [tailwind-merge](https://github.com/dcastil/tailwind-merge) | class names | MIT |
| [Vite](https://vite.dev) | the build | MIT |

The panel loads nothing from a CDN: every asset is built into `internal/admin/ui/dist` and
embedded in the binary. The fonts are the system stack; no font file is shipped.

The design follows the
[ui-ux-pro-max-skill](https://github.com/nextlevelbuilder/ui-ux-pro-max-skill) reference (MIT);
no code is copied from it, only its token architecture and design rules.
