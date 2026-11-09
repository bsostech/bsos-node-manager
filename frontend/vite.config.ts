import { defineConfig, type Plugin } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';
import path from 'node:path';

/**
 * npm run dev:core: the dev server treats src/enterprise/ as absent, so you can see what Core looks like without exporting the open-source edition.
 * Before Vite processes import.meta.glob, globs pointing at enterprise are replaced with an empty object, so the modules are never loaded.
 * Only applies in the dev server's core mode; production builds still just check whether the directory exists.
 */
function coreEdition(): Plugin {
  const glob = /import\.meta\.glob(?:<[^(]*>)?\s*\(([^)]*)\)/g;
  return {
    name: 'bsos-core-edition',
    enforce: 'pre',
    apply: (_, env) => env.command === 'serve' && env.mode === 'core',
    transform(code, id) {
      if (!id.includes('/src/') || !code.includes('import.meta.glob')) return;
      const out = code.replace(glob, (call, args: string) => (args.includes('enterprise/') ? '({})' : call));
      return out === code ? undefined : { code: out, map: null };
    },
  };
}

export default defineConfig({
  plugins: [coreEdition(), react(), tailwindcss()],
  resolve: {
    alias: { '@': path.resolve(__dirname, 'src') },
  },
  server: { port: 5173 },
});
