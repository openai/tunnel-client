# Tunnel Guide Visual Manifest

This file tracks the decorative artwork prompts and the committed assets used by
`docs/end-user-guide.md`.

The guide uses committed local SVG assets so the HTML archive and slide-page render path stays
deterministic in CI and in the public mirror. The prompts below record the intended art direction so a future
contributor can regenerate or replace the visuals intentionally instead of guessing.

## Cover art

- File: `docs/images/generated/cover-tunnel-orbit-v1.svg`
- Used in: the opening hero block of `docs/end-user-guide.md`
- Prompt record:
  "Warm technical poster illustration for a tunnel onboarding guide. Show a private MCP server as
  a grounded destination, colorful operator surfaces on the other side, and one confident secure
  path between them. Use teal, navy, amber, and coral accents. Avoid product UI mimicry, avoid
  logos, avoid photorealism, and keep the result abstract enough to stay customer-safe."

## Permissions and keys divider

- File: `docs/images/generated/divider-keys-v1.svg`
- Used in: the `Before you start` section
- Prompt record:
  "Abstract section divider for permissions, groups, keys, and tunnel identifiers. Use labeled
  capsule shapes, dotted routes, and a technical-but-friendly palette that matches the cover."

## Local UI divider

- File: `docs/images/generated/divider-ui-v1.svg`
- Used in: the `Check the local UI` section
- Prompt record:
  "Abstract section divider for a local dashboard, metrics, logs, and health surfaces. Use screen
  panes, chart marks, and a calm operations palette. Keep it simple enough to print cleanly."

## Codex divider

- File: `docs/images/generated/divider-codex-v1.svg`
- Used in: the `Use it from Codex` section
- Prompt record:
  "Abstract section divider for a prompt window, runtime bridge, and operator automation. Use
  directional motion lines and console-inspired cards without imitating any real product UI."

## Page background set

- Files:
  `docs/images/generated/pdf-pages/page-cover-v1.webp`,
  `docs/images/generated/pdf-pages/page-interior-v1.webp`,
  `docs/images/generated/pdf-pages/page-code-v1.webp`,
  `docs/images/generated/pdf-pages/page-screenshots-v1.webp`
- Used in: `docs/pdf/end_user_guide_page_blocks.py` for the rendered page-image set that feeds the
  slide deck
- Prompt record:
  "Editorial print background system for a tunnel onboarding guide. Create four related page
  backgrounds: a bold cover, a quiet interior page, a console-oriented code page, and a
  screenshot-supporting page. Use warm paper, teal, navy, amber, and subtle grid or glow accents.
  Leave broad calm zones for overlaid text and screenshots. Keep the art abstract, customer-safe,
  and non-product-specific."

## Asset policy

- Keep decorative assets abstract. Do not use generated art as a substitute for real product
  screenshots.
- If the visual direction changes materially, write a new versioned filename first, then update the
  guide references after review.
- Keep future visual dependencies local to the repo. Do not add remote image URLs to the guide.
