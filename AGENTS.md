<!-- BEGIN:nextjs-agent-rules -->
# This is NOT the Next.js you know

This version has breaking changes — APIs, conventions, and file structure may all differ from your training data. Read the relevant guide in `node_modules/next/dist/docs/` before writing any code. Heed deprecation notices.
<!-- END:nextjs-agent-rules -->

# Project Architecture & Conventions

- **Next.js 16 App Router**: Uses the root `app/` directory (there is no `src/` folder).
- **Custom Port**: The development and start servers run on port **4000** (`npm run dev`), not the default 3000.
- **Route Groups**: The `app/` structure uses route groups heavily, such as `app/(archive)/`.
- **Path Aliases**: The `@/*` path alias resolves to the project root `./*` (e.g., `@/app/...`), not a `src/` directory.
- **Styling**: Tailwind CSS v4 via `@tailwindcss/postcss`.