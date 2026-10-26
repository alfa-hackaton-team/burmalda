import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    strictPort: true,
    proxy: { '/v0.1': 'http://127.0.0.1:18080' },
  },
  test: { environment: 'jsdom', setupFiles: './src/test-setup.ts', restoreMocks: true },
});
