import { defineConfig } from 'tsup';

export default defineConfig({
  entry: ['src/index.ts', 'src/react.ts', 'src/stimulus.ts'],
  external: ['react', '@hotwired/stimulus'],
  format: ['esm', 'cjs'],
  dts: true,
  clean: true,
  sourcemap: true,
  target: 'es2022',
  platform: 'browser',
});
