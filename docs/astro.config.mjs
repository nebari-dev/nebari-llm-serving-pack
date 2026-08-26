// docs/astro.config.mjs
import { defineConfig } from 'astro/config';
import { fileURLToPath } from 'node:url';
import starlight from '@astrojs/starlight';
import { nebari } from '@nebari/starlight';
import rehypeMermaid from 'rehype-mermaid';
import remarkBaseLinks from './src/plugins/remark-base-links';

export default defineConfig({
  // Base defaults to '/' for the local bake-off. Override via BASE env if deployed.
  base: process.env.BASE || '/',
  site: process.env.SITE,
  integrations: [
    starlight({
      title: 'LLM Serving Pack',
      description:
        'Kubernetes operator for serving LLMs on Nebari via llm-d, with per-model OIDC access control, API key management, and Envoy AI Gateway integration.',
      // Shared Nebari identity (brand colors, fonts, logo, favicon, footer, and
      // GitHub social link) comes from the @nebari/starlight theme plugin. The
      // header logo returns users to the pack catalog and the GitHub icon
      // opens this pack's repository.
      plugins: [
        nebari({
          logoHref: 'https://packs.nebari.dev/',
          githubHref: 'https://github.com/nebari-dev/llm-serving-pack',
        }),
      ],
      sidebar: [
        {
          label: 'Documentation',
          items: [
            { label: 'Quickstart', slug: 'quickstart' },
            { label: 'Installation', slug: 'installation' },
            { label: 'Local Development', slug: 'local-development' },
            { label: 'UI Development', slug: 'ui-development' },
            { label: 'Shared Storage', slug: 'shared-storage' },
            { label: 'Troubleshooting', slug: 'troubleshooting' },
          ],
        },
        {
          label: 'Reference',
          items: [
            { label: 'Configuration', slug: 'configuration' },
            { label: 'Architecture', slug: 'architecture' },
            { label: 'CI/CD and Releasing', slug: 'cicd-and-releasing' },
          ],
        },
      ],
    }),
  ],
  markdown: {
    // syntaxHighlight false on mermaid so the plugin sees raw graph source.
    syntaxHighlight: { type: 'shiki', excludeLangs: ['mermaid'] },
    remarkPlugins: [[remarkBaseLinks, { base: process.env.BASE || '/' }]],
    rehypePlugins: [[rehypeMermaid, { strategy: 'inline-svg' }]],
  },
  vite: {
    server: {
      fs: {
        // Defense-in-depth: allow the repo root (parent of docs/) so Vite's
        // dev static-file middleware can serve examples/*.yaml if a future
        // pattern ever needs it. The current embed resolves examples/*.yaml
        // via the Astro SSR module graph (`?raw` import), which is NOT gated
        // by fs.allow, so this is precautionary, not load-bearing today.
        allow: [fileURLToPath(new URL('..', import.meta.url))],
      },
    },
  },
});
