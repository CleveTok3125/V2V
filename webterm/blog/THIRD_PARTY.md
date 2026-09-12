# Third-party design source: astro-theme-cactus

- Upstream: https://github.com/chrismwilliams/astro-theme-cactus
- Pinned commit: `210d96d7e286535f183d34d60afdbb9f28b52df1` (v8.2.0 era)
- License: MIT, Copyright (c) 2022 Chris Williams.
  Permission is hereby granted, free of charge, to any person obtaining a
  copy of this software and associated documentation files (the "Software"),
  to deal in the Software without restriction, including without limitation
  the rights to use, copy, modify, merge, publish, distribute, sublicense,
  and/or sell copies of the Software, and to permit persons to whom the
  Software is furnished to do so, subject to the following conditions:
  The above copyright notice and this permission notice shall be included in
  all copies or substantial portions of the Software.

## What was taken

`cactus.css` is a static port (no Tailwind build) of:

- `src/styles/global.css` (oklch tokens, `@property` registrations,
  `[data-theme]` light/dark switch, base body rules)
- Prose rules from `tailwind.config.ts` typography extension
  (headings, links, blockquote, code, hr, tables, task lists)
- Layout proportions from `src/layouts/Base.astro` (`max-w-3xl`) and
  `src/layouts/BlogPost.astro` (masthead, back-to-top)

## What was changed from upstream

- Palette retuned to the login screen (`webterm/terminal.css`):
  dark `#101014`/`#d8dee8`/`#8b93a5`/`#4f7dff`, light lifted same hue.
- Removed: `#` prefixes on headings/tags/TOC, about/notes/search/
  webmentions/social, logo, footer slimmed to exact `© V2V 2026`.
- Body 16px instead of theme `text-sm`.
- Code token colors bound to theme variables instead of expressive-code
  themes (see `server/blog` render templates).

## Update procedure

1. `git clone --depth N` upstream, `git log -- <files above>`.
2. Re-port worthwhile deltas into `cactus.css` by hand.
3. Bump the pinned commit hash in this file.
